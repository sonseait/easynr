package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"easynr/internal/auth"
	bolt "go.etcd.io/bbolt"
)

const maxMetadata = 32 << 20
const installMediaType = "application/vnd.npm.install-v1+json"

func artifactID(a Artifact) string {
	b, _ := json.Marshal([]string{a.Package, a.Version, a.Source, a.URL, a.Integrity, a.Shasum})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func artifactPath(id string) string { return "/-/tarballs/" + id + ".tgz" }
func (e *Engine) local(name string) (LocalPackage, error) {
	var p LocalPackage
	err := e.read("packages", name, &p)
	return p, err
}
func (e *Engine) remote(ctx context.Context, name string, force, abbreviated bool) (Cached, error) {
	u, err := e.upstream(name)
	if err != nil {
		return Cached{}, err
	}
	key := u.ID + ":" + name
	if abbreviated {
		key = u.ID + ":install:" + name
	}
	result, err, _ := e.flights.Do("metadata:"+key, func() (result any, err error) {
		var cached Cached
		ce := e.read("cache", key, &cached)
		if ce != nil && !errors.Is(ce, ErrNotFound) {
			return nil, ce
		}
		// Existing full caches remain usable for installs after upgrading. Either
		// representation can supply versions offline; validators stay separate.
		fallback, haveFallback := cached, ce == nil
		if errors.Is(ce, ErrNotFound) {
			fallbackKey := u.ID + ":install:" + name
			if abbreviated {
				fallbackKey = u.ID + ":" + name
			}
			fallbackErr := e.read("cache", fallbackKey, &fallback)
			if fallbackErr != nil && !errors.Is(fallbackErr, ErrNotFound) {
				return nil, fallbackErr
			}
			haveFallback = fallbackErr == nil
			if abbreviated && haveFallback && !force && e.Now().Sub(fallback.FetchedAt) < e.TTL {
				return fallback, nil
			}
		}
		defer func() {
			if haveFallback && errors.Is(err, ErrUpstream) {
				result, err = fallback, nil
			}
		}()
		if ce == nil && !force && e.Now().Sub(cached.FetchedAt) < e.TTL {
			return cached, nil
		}
		raw := u.URL + "/" + url.PathEscape(name)
		req, re := e.request(ctx, u, raw)
		if re != nil {
			return nil, re
		}
		req.Header.Set("Accept", "application/json")
		if abbreviated {
			req.Header.Set("Accept", installMediaType)
		}
		if ce == nil {
			if cached.ETag != "" {
				req.Header.Set("If-None-Match", cached.ETag)
			}
			if cached.Modified != "" {
				req.Header.Set("If-Modified-Since", cached.Modified)
			}
		}
		resp, re := e.Client.Do(req)
		if re != nil {
			if ce == nil {
				return cached, nil
			}
			return nil, ErrUpstream
		}
		defer resp.Body.Close()
		if resp.StatusCode == 304 && ce == nil {
			cached.FetchedAt = e.Now()
			e.mu.RLock()
			err := e.Store.DB.Update(func(tx *bolt.Tx) error {
				if tx.Bucket([]byte("cache")).Get([]byte(key)) == nil {
					return ErrNotFound
				}
				return put(tx, "cache", key, cached)
			})
			e.mu.RUnlock()
			if err != nil {
				return nil, err
			}
			return cached, nil
		}
		if resp.StatusCode == 404 {
			return nil, ErrNotFound
		}
		if resp.StatusCode != 200 {
			if ce == nil {
				return cached, nil
			}
			return nil, ErrUpstream
		}
		body, re := io.ReadAll(io.LimitReader(resp.Body, maxMetadata+1))
		if re != nil || len(body) > maxMetadata {
			if ce == nil {
				return cached, nil
			}
			return nil, ErrUpstream
		}
		var doc Document
		if json.Unmarshal(body, &doc) != nil || doc == nil || str(doc["name"]) != name {
			return nil, ErrUpstream
		}
		versions := object(doc["versions"])
		if len(versions) == 0 {
			return nil, ErrNotFound
		}
		artifacts := []Artifact{}
		rewritten := Document{}
		for version, v := range versions {
			item := object(v)
			dist := object(item["dist"])
			rawURL := str(dist["tarball"])
			target, parseErr := url.Parse(rawURL)
			if parseErr != nil || target.User != nil {
				return nil, ErrUpstream
			}
			if !target.IsAbs() {
				target = req.URL.ResolveReference(target)
			}
			// Credentials in upstream URLs are forbidden; redirects receive no foreign token.
			if _, err := validateTarget(target.String()); err != nil {
				return nil, ErrUpstream
			}
			a := Artifact{Package: name, Version: version, Source: u.ID, URL: target.String(), Integrity: str(dist["integrity"]), Shasum: str(dist["shasum"])}
			if a.Integrity == "" && a.Shasum == "" {
				return nil, fmt.Errorf("%w: missing tarball checksum", ErrUpstream)
			}
			a.ID = artifactID(a)
			artifacts = append(artifacts, a)
			dist["tarball"] = artifactPath(a.ID)
			item["dist"] = dist
			rewritten[version] = item
		}
		doc["versions"] = rewritten
		delete(doc, "_attachments")
		next := Cached{Upstream: u.ID, Name: name, Document: doc, ETag: resp.Header.Get("ETag"), Modified: resp.Header.Get("Last-Modified"), FetchedAt: e.Now()}
		// A cache clear and refresh cannot interleave their reference updates.
		e.mu.RLock()
		defer e.mu.RUnlock()
		err = e.Store.DB.Update(func(tx *bolt.Tx) error {
			for _, a := range artifacts {
				var previous Artifact
				if err := get(tx, "artifacts", a.ID, &previous); err == nil {
					a.Hash = previous.Hash
					a.Size = previous.Size
				}
				if err := put(tx, "artifacts", a.ID, a); err != nil {
					return err
				}
			}
			return put(tx, "cache", key, next)
		})
		return next, err
	})
	if err != nil {
		return Cached{}, err
	}
	return result.(Cached), nil
}

// Metadata merges whole version entries, never fields from different sources.
func (e *Engine) Metadata(ctx context.Context, name string, force bool) (Document, error) {
	return e.metadata(ctx, name, force, false)
}

func (e *Engine) metadata(ctx context.Context, name string, force, abbreviated bool) (Document, error) {
	if !auth.ValidPackage(name) {
		return nil, ErrInvalid
	}
	p, localErr := e.local(name)
	if localErr != nil && !errors.Is(localErr, ErrNotFound) {
		return nil, localErr
	}
	c, remoteErr := e.remote(ctx, name, force, abbreviated)
	if remoteErr != nil && localErr != nil {
		return nil, remoteErr
	}
	doc := Document{"name": name}
	versions := Document{}
	tags := Document{}
	if remoteErr == nil {
		for k, v := range c.Document {
			doc[k] = v
		}
		for k, v := range object(c.Document["versions"]) {
			versions[k] = v
		}
		for k, v := range object(c.Document["dist-tags"]) {
			tags[k] = v
		}
	}
	if localErr == nil {
		for k, v := range p.Versions {
			versions[k] = v
		}
		for k, v := range p.Tags {
			tags[k] = v
		}
	}
	if len(versions) == 0 {
		return nil, ErrNotFound
	}
	doc["name"] = name
	doc["versions"] = versions
	// Never expose dangling tags after removing a local version.
	for tag, version := range tags {
		if _, ok := versions[str(version)]; !ok {
			delete(tags, tag)
		}
	}
	doc["dist-tags"] = tags
	return doc, nil
}
func render(doc Document, base string) Document {
	// Copy only maps whose fields change. Nested metadata is immutable and may
	// be shared by coalesced requests; a JSON round trip duplicates the packument.
	copy := cloneDocument(doc)
	versions := Document{}
	for version, v := range object(doc["versions"]) {
		item := cloneDocument(object(v))
		dist := cloneDocument(object(item["dist"]))
		path := str(dist["tarball"])
		if strings.HasPrefix(path, "/-/tarballs/") {
			dist["tarball"] = base + path
		}
		item["dist"] = dist
		versions[version] = item
	}
	copy["versions"] = versions
	return copy
}

func cloneDocument(doc Document) Document {
	copy := make(Document, len(doc))
	for k, v := range doc {
		copy[k] = v
	}
	return copy
}

type PackageInfo struct {
	Name           string     `json:"name"`
	LocalVersions  int        `json:"localVersions"`
	CachedVersions int        `json:"cachedVersions"`
	CachedBytes    int64      `json:"cachedBytes"`
	LocalBytes     int64      `json:"localBytes"`
	UpdatedAt      *time.Time `json:"updatedAt,omitempty"`
}

func (e *Engine) Packages() ([]PackageInfo, error) {
	items := map[string]*PackageInfo{}
	ids := map[string]bool{}
	counted := map[string]bool{}
	ensure := func(name string) *PackageInfo {
		if items[name] == nil {
			items[name] = &PackageInfo{Name: name}
		}
		return items[name]
	}
	mark := func(versions Document) {
		for _, v := range versions {
			path := str(object(object(v)["dist"])["tarball"])
			id := strings.TrimSuffix(strings.TrimPrefix(path, "/-/tarballs/"), ".tgz")
			ids[id] = true
		}
	}
	err := e.Store.DB.View(func(tx *bolt.Tx) error {
		if err := tx.Bucket([]byte("packages")).ForEach(func(k, v []byte) error {
			var p LocalPackage
			if err := json.Unmarshal(v, &p); err != nil {
				return err
			}
			i := ensure(p.Name)
			i.LocalVersions = len(p.Versions)
			i.UpdatedAt = &p.UpdatedAt
			versions := Document{}
			for v, d := range p.Versions {
				versions[v] = d
			}
			mark(versions)
			return nil
		}); err != nil {
			return err
		}
		if err := tx.Bucket([]byte("cache")).ForEach(func(k, v []byte) error {
			// Most cached packages are remote-only. Do not decode their (possibly
			// multi-megabyte) metadata to build the local package list.
			if items[cachePackageName(k)] == nil {
				return nil
			}
			var c Cached
			if err := decodeCache(v, &c); err != nil {
				return err
			}
			id := "npmjs"
			routes := tx.Bucket([]byte("routes"))
			if r := routes.Get([]byte("*")); r != nil {
				id = string(r)
			}
			if strings.HasPrefix(c.Name, "@") {
				if r := routes.Get([]byte(strings.SplitN(c.Name, "/", 2)[0])); r != nil {
					id = string(r)
				}
			}
			if item := items[c.Name]; item != nil && id == c.Upstream {
				item.CachedVersions = len(object(c.Document["versions"]))
				mark(object(c.Document["versions"]))
			}
			return nil
		}); err != nil {
			return err
		}
		return tx.Bucket([]byte("artifacts")).ForEach(func(k, v []byte) error {
			if !ids[string(k)] {
				return nil
			}
			var a Artifact
			if err := json.Unmarshal(v, &a); err != nil {
				return err
			}
			category := "remote"
			if a.Source == "local" {
				category = "local"
			}
			key := a.Package + ":" + category + ":" + a.Hash
			if a.Hash != "" && !counted[key] {
				counted[key] = true
				i := ensure(a.Package)
				if category == "local" {
					i.LocalBytes += a.Size
				} else {
					i.CachedBytes += a.Size
				}
			}
			return nil
		})
	})
	result := []PackageInfo{}
	for _, i := range items {
		result = append(result, *i)
	}
	sortPackages(result)
	return result, err
}
func (e *Engine) DeleteLocal(name, version string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.Store.DB.Update(func(tx *bolt.Tx) error {
		var p LocalPackage
		if err := get(tx, "packages", name, &p); err != nil {
			return err
		}
		if version == "" {
			return tx.Bucket([]byte("packages")).Delete([]byte(name))
		}
		if _, ok := p.Versions[version]; !ok {
			return ErrNotFound
		}
		delete(p.Versions, version)
		for t, v := range p.Tags {
			if v == version {
				delete(p.Tags, t)
			}
		}
		p.UpdatedAt = e.Now()
		if len(p.Versions) == 0 {
			return tx.Bucket([]byte("packages")).Delete([]byte(name))
		}
		return put(tx, "packages", name, p)
	})
}
func (e *Engine) ClearCache(name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.Store.DB.Update(func(tx *bolt.Tx) error {
		for _, bucket := range []string{"cache", "artifacts"} {
			b := tx.Bucket([]byte(bucket))
			c := b.Cursor()
			for k, v := c.First(); k != nil; k, v = c.Next() {
				remove := false
				if bucket == "cache" {
					remove = name == "" || cachePackageName(k) == name
				} else {
					var a Artifact
					if err := json.Unmarshal(v, &a); err != nil {
						return err
					}
					remove = a.Source != "local" && (name == "" || a.Package == name)
				}
				if remove {
					if err := c.Delete(); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
}

// Inspect adds provenance for the admin console without exposing upstream credentials or URLs.
func (e *Engine) Inspect(ctx context.Context, name string) (Document, error) {
	doc, err := e.Metadata(ctx, name, false)
	if err != nil {
		return nil, err
	}
	sources := Document{}
	err = e.Store.DB.View(func(tx *bolt.Tx) error {
		for v, item := range object(doc["versions"]) {
			path := str(object(object(item)["dist"])["tarball"])
			id := strings.TrimSuffix(strings.TrimPrefix(path, "/-/tarballs/"), ".tgz")
			var a Artifact
			if err := get(tx, "artifacts", id, &a); err != nil {
				return err
			}
			sources[v] = Document{"source": a.Source, "cached": a.Hash != "", "bytes": a.Size}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	doc["_easynr"] = sources
	return doc, nil
}
