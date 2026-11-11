package registry

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"easynr/internal/auth"
)

var zeroTime time.Time

func wantsInstallMetadata(accept string) bool {
	var install, full float64
	for _, entry := range strings.Split(accept, ",") {
		media, params, err := mime.ParseMediaType(strings.TrimSpace(entry))
		if err != nil {
			continue
		}
		q := 1.0
		if value, ok := params["q"]; ok {
			q, err = strconv.ParseFloat(value, 64)
			if err != nil || q < 0 || q > 1 {
				continue
			}
		}
		switch media {
		case installMediaType:
			install = q
		case "application/json":
			full = q
		}
	}
	return install > 0 && install >= full
}

func npmJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if status != 204 {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func npmMetadata(w http.ResponseWriter, r *http.Request, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		npmError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, max-age=300, must-revalidate")
	w.Header().Set("ETag", fmt.Sprintf(`"%x"`, sha256.Sum256(body)))
	http.ServeContent(w, r, "metadata.json", zeroTime, bytes.NewReader(body))
}

func npmError(w http.ResponseWriter, err error) {
	status, code, message := 500, "EINTERNAL", "registry operation failed"
	switch {
	case errors.Is(err, ErrNotFound):
		status, code, message = 404, "E404", "package, version or artifact not found"
	case errors.Is(err, ErrInvalid):
		status, code, message = 400, "EINVALID", "invalid package, version, tag or publish payload"
	case errors.Is(err, ErrConflict):
		status, code, message = 409, "EPUBLISHCONFLICT", "local version already exists or resource is in use"
	case errors.Is(err, ErrUpstream):
		status, code, message = 502, "EUPSTREAM", "upstream unavailable or invalid response"
	}
	npmJSON(w, status, map[string]string{"error": code, "reason": message})
}
func (e *Engine) base(r *http.Request) string {
	if e.PublicURL != "" {
		return e.PublicURL
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
func (e *Engine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	credential := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if credential == r.Header.Get("Authorization") {
		credential = ""
	}
	token, err := e.Auth.Authenticate(credential)
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Bearer realm="EasyNR"`)
		npmJSON(w, 401, map[string]string{"error": "ENEEDAUTH", "reason": "valid npm token required"})
		return
	}
	path := strings.Trim(r.URL.Path, "/")
	if path == "-/ping" {
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		npmJSON(w, 200, map[string]any{})
		return
	}
	if path == "-/whoami" {
		npmJSON(w, 200, map[string]string{"username": token.Name})
		return
	}
	if strings.HasPrefix(path, "-/tarballs/") {
		if r.Method != "GET" && r.Method != "HEAD" {
			w.WriteHeader(405)
			return
		}
		id := strings.TrimSuffix(strings.TrimPrefix(path, "-/tarballs/"), ".tgz")
		if len(id) != 64 || strings.Trim(id, "0123456789abcdef") != "" {
			npmError(w, ErrNotFound)
			return
		}
		var a Artifact
		if err := e.read("artifacts", id, &a); err != nil {
			npmError(w, err)
			return
		}
		if !auth.Allows(token.Grants, a.Package, "read") {
			npmJSON(w, 403, map[string]string{"error": "E403"})
			return
		}
		a, err = e.Blob(r.Context(), id)
		if err != nil {
			npmError(w, err)
			return
		}
		f, err := os.Open(e.blobPath(a.Hash))
		if err != nil {
			npmError(w, ErrNotFound)
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
		w.Header().Set("ETag", `"`+a.Hash+`"`)
		http.ServeContent(w, r, a.Package+".tgz", zeroTime, f)
		return
	}
	if strings.HasPrefix(path, "-/package/") {
		tail := strings.TrimPrefix(path, "-/package/")
		name, tag, ok := strings.Cut(tail, "/dist-tags")
		if !ok || !auth.ValidPackage(name) {
			npmError(w, ErrNotFound)
			return
		}
		action := "read"
		if r.Method != "GET" {
			action = "write"
		}
		if !auth.Allows(token.Grants, name, action) {
			npmJSON(w, 403, map[string]string{"error": "E403"})
			return
		}
		tag = strings.TrimPrefix(tag, "/")
		if r.Method == "GET" {
			doc, err := e.Metadata(r.Context(), name, false)
			if err != nil {
				npmError(w, err)
				return
			}
			npmMetadata(w, r, doc["dist-tags"])
			return
		}
		if r.Method != "PUT" && r.Method != "DELETE" {
			w.WriteHeader(405)
			return
		}
		version := ""
		if r.Method == "PUT" {
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
			if err != nil || json.Unmarshal(body, &version) != nil {
				npmError(w, ErrInvalid)
				return
			}
		}
		if err := e.SetTag(name, tag, version, r.Method == "DELETE"); err != nil {
			npmError(w, err)
			return
		}
		npmJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	if strings.HasPrefix(path, "-/") {
		npmError(w, ErrNotFound)
		return
	}
	// URL.Path is already decoded, including npm's @scope%2fname encoding.
	parts := strings.Split(path, "/")
	name := ""
	selector := ""
	if strings.HasPrefix(path, "@") {
		if len(parts) < 2 || len(parts) > 3 {
			npmError(w, ErrInvalid)
			return
		}
		name = parts[0] + "/" + parts[1]
		if len(parts) == 3 {
			selector = parts[2]
		}
	} else {
		if len(parts) > 2 {
			npmError(w, ErrInvalid)
			return
		}
		name = parts[0]
		if len(parts) == 2 {
			selector = parts[1]
		}
	}
	if !auth.ValidPackage(name) {
		npmError(w, ErrInvalid)
		return
	}
	action := "read"
	if r.Method != "GET" && r.Method != "HEAD" {
		action = "write"
	}
	if !auth.Allows(token.Grants, name, action) {
		npmJSON(w, 403, map[string]string{"error": "E403"})
		return
	}
	switch r.Method {
	case "GET", "HEAD":
		// Hold the slot through encoding, so a burst of dependency requests cannot
		// keep an unbounded number of decoded packuments alive simultaneously.
		select {
		case e.metadataSlots <- struct{}{}:
			defer func() { <-e.metadataSlots }()
		case <-r.Context().Done():
			return
		}
		abbreviated := wantsInstallMetadata(r.Header.Get("Accept"))
		w.Header().Add("Vary", "Accept")
		// A known local version/tag can be served without consulting any remote.
		if selector != "" {
			p, err := e.local(name)
			if err == nil {
				version := selector
				if tag := p.Tags[selector]; tag != "" {
					version = tag
				}
				if item, ok := p.Versions[version]; ok {
					rendered := render(Document{"versions": Document{version: item}}, e.base(r))
					npmMetadata(w, r, object(rendered["versions"])[version])
					return
				}
			} else if !errors.Is(err, ErrNotFound) {
				npmError(w, err)
				return
			}
		}
		doc, err := e.metadata(r.Context(), name, false, abbreviated)
		if err != nil {
			npmError(w, err)
			return
		}
		if selector != "" {
			versions := object(doc["versions"])
			version := selector
			if t := str(object(doc["dist-tags"])[selector]); t != "" {
				version = t
			}
			if versions[version] == nil {
				doc, err = e.metadata(r.Context(), name, true, abbreviated)
				if err != nil {
					npmError(w, err)
					return
				}
				versions = object(doc["versions"])
				version = selector
				if t := str(object(doc["dist-tags"])[selector]); t != "" {
					version = t
				}
			}
			if versions[version] == nil {
				npmError(w, ErrNotFound)
				return
			}
			rendered := render(Document{"versions": Document{version: versions[version]}}, e.base(r))
			npmMetadata(w, r, object(rendered["versions"])[version])
			return
		}
		npmMetadata(w, r, render(doc, e.base(r)))
	case "PUT":
		if selector != "" {
			npmError(w, ErrInvalid)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPublish))
		if err != nil {
			npmJSON(w, 413, map[string]string{"error": "ETOOLARGE"})
			return
		}
		if err = e.Publish(name, body); err != nil {
			npmError(w, err)
			return
		}
		npmJSON(w, 201, map[string]bool{"ok": true})
	default:
		w.WriteHeader(405)
	}
}
