// Package registry implements a filesystem-backed npm registry and scoped proxy.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"easynr/internal/auth"
	"easynr/internal/state"
	bolt "go.etcd.io/bbolt"
	"golang.org/x/sync/singleflight"
)

var ErrNotFound = errors.New("not found")
var ErrConflict = errors.New("conflict")
var ErrInvalid = errors.New("invalid input")
var ErrUpstream = errors.New("upstream unavailable")

type Document map[string]any

type LocalPackage struct {
	Name      string              `json:"name"`
	Versions  map[string]Document `json:"versions"`
	Tags      map[string]string   `json:"dist-tags"`
	UpdatedAt time.Time           `json:"updatedAt"`
}
type Cached struct {
	Upstream  string    `json:"upstream"`
	Name      string    `json:"name"`
	Document  Document  `json:"document"`
	ETag      string    `json:"etag,omitempty"`
	Modified  string    `json:"modified,omitempty"`
	FetchedAt time.Time `json:"fetchedAt"`
}
type Artifact struct {
	ID        string `json:"id"`
	Package   string `json:"package"`
	Version   string `json:"version"`
	Source    string `json:"source"`
	URL       string `json:"url,omitempty"`
	Integrity string `json:"integrity,omitempty"`
	Shasum    string `json:"shasum,omitempty"`
	Hash      string `json:"hash,omitempty"`
	Size      int64  `json:"size"`
}
type Upstream struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	URL   string `json:"url"`
	Token string `json:"token,omitempty"`
}
type Route struct {
	Scope    string `json:"scope"`
	Upstream string `json:"upstream"`
}
type Engine struct {
	Store          *state.Store
	Auth           auth.Service
	Dir, PublicURL string
	Client         *http.Client
	TTL            time.Duration
	Now            func() time.Time
	flights        singleflight.Group
	metadataSlots  chan struct{}
	downloadSlots  chan struct{}
	mu             sync.RWMutex // serializes mutations; GC only runs offline
}

func New(s *state.Store, dir, publicURL string) (*Engine, error) {
	if s == nil {
		return nil, errors.New("store required")
	}
	for _, p := range []string{"blobs", "tmp"} {
		if err := os.MkdirAll(filepath.Join(dir, p), 0700); err != nil {
			return nil, err
		}
	}
	e := &Engine{Store: s, Auth: auth.Service{Store: s}, Dir: dir, PublicURL: publicURL, TTL: 5 * time.Minute, Now: time.Now,
		metadataSlots: make(chan struct{}, 4), downloadSlots: make(chan struct{}, 8)}
	// Redirects never forward credentials to a different origin.
	e.Client = &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if r.URL.User != nil || (r.URL.Scheme != "http" && r.URL.Scheme != "https") {
			return errors.New("invalid redirect scheme")
		}
		if len(via) > 0 && via[0].URL.Scheme == "https" && r.URL.Scheme != "https" {
			return errors.New("insecure redirect")
		}
		if len(via) > 0 && !sameOrigin(r.URL, via[0].URL) {
			r.Header.Del("Authorization")
		}
		return nil
	}}
	var existing Upstream
	err := e.read("upstreams", "npmjs", &existing)
	if errors.Is(err, ErrNotFound) {
		err = s.DB.Update(func(tx *bolt.Tx) error {
			return put(tx, "upstreams", "npmjs", Upstream{ID: "npmjs", Name: "npmjs", URL: "https://registry.npmjs.org"})
		})
	}
	return e, err
}
func get(tx *bolt.Tx, bucket, key string, out any) error {
	v := tx.Bucket([]byte(bucket)).Get([]byte(key))
	if v == nil {
		return ErrNotFound
	}
	if bucket == "cache" {
		return decodeCache(v, out)
	}
	return json.Unmarshal(v, out)
}
func put(tx *bolt.Tx, bucket, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if bucket == "cache" {
		b, err = compressCache(b)
		if err != nil {
			return err
		}
	}
	return tx.Bucket([]byte(bucket)).Put([]byte(key), b)
}
func (e *Engine) read(bucket, key string, v any) error {
	return e.Store.DB.View(func(tx *bolt.Tx) error { return get(tx, bucket, key, v) })
}
func (e *Engine) write(bucket, key string, v any) error {
	return e.Store.DB.Update(func(tx *bolt.Tx) error { return put(tx, bucket, key, v) })
}
func object(v any) Document {
	if d, ok := v.(map[string]any); ok {
		return Document(d)
	}
	if d, ok := v.(Document); ok {
		return d
	}
	return Document{}
}
func str(v any) string { s, _ := v.(string); return s }
func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}
func validateTarget(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Fragment != "" {
		return nil, fmt.Errorf("%w: http(s) URL required without credentials or fragment", ErrInvalid)
	}
	return u, nil
}
func validateURL(raw string) (*url.URL, error) {
	u, err := validateTarget(raw)
	if err != nil {
		return nil, err
	}
	if u.RawQuery != "" {
		return nil, fmt.Errorf("%w: upstream URL must not contain a query", ErrInvalid)
	}
	return u, nil
}
func (e *Engine) Upstreams() ([]Upstream, error) {
	items := []Upstream{}
	err := e.Store.DB.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("upstreams")).ForEach(func(k, v []byte) error {
			var u Upstream
			if err := json.Unmarshal(v, &u); err != nil {
				return err
			}
			items = append(items, u)
			return nil
		})
	})
	return items, err
}
func (e *Engine) SaveUpstream(u Upstream) error {
	if u.ID == "local" || !auth.ValidPackage(u.ID) || strings.ContainsAny(u.ID, "/@") || strings.TrimSpace(u.Name) == "" {
		return ErrInvalid
	}
	if _, err := validateURL(u.URL); err != nil {
		return err
	}
	u.URL = strings.TrimRight(u.URL, "/")
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.Store.DB.Update(func(tx *bolt.Tx) error {
		var old Upstream
		settings := tx.Bucket([]byte("settings"))
		identity := []byte("upstream_url:" + u.ID)
		if previous := settings.Get(identity); previous != nil && string(previous) != u.URL {
			return fmt.Errorf("%w: upstream IDs cannot be reused for a different URL", ErrConflict)
		}
		if err := get(tx, "upstreams", u.ID, &old); err == nil {
			if old.URL != u.URL {
				return fmt.Errorf("%w: URL is immutable; create a new upstream", ErrConflict)
			}
			if u.Token == "" {
				u.Token = old.Token
			} // empty preserves; '-' clears
		}
		if u.Token == "-" {
			u.Token = ""
		}
		if err := settings.Put(identity, []byte(u.URL)); err != nil {
			return err
		}
		return put(tx, "upstreams", u.ID, u)
	})
}
func (e *Engine) Routes() ([]Route, error) {
	items := []Route{}
	err := e.Store.DB.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("routes")).ForEach(func(k, v []byte) error { items = append(items, Route{string(k), string(v)}); return nil })
	})
	return items, err
}
func (e *Engine) SaveRoute(r Route) error {
	if r.Scope != "*" && (!strings.HasPrefix(r.Scope, "@") || !auth.ValidPackage(r.Scope+"/x")) {
		return ErrInvalid
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.Store.DB.Update(func(tx *bolt.Tx) error {
		if tx.Bucket([]byte("upstreams")).Get([]byte(r.Upstream)) == nil {
			return ErrInvalid
		}
		return tx.Bucket([]byte("routes")).Put([]byte(r.Scope), []byte(r.Upstream))
	})
}
func (e *Engine) DeleteRoute(scope string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.Store.DB.Update(func(tx *bolt.Tx) error { return tx.Bucket([]byte("routes")).Delete([]byte(scope)) })
}
func (e *Engine) upstream(name string) (Upstream, error) {
	var u Upstream
	err := e.Store.DB.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("routes"))
		id := string(b.Get([]byte("*")))
		if id == "" {
			id = "npmjs"
		}
		if strings.HasPrefix(name, "@") {
			scope := strings.SplitN(name, "/", 2)[0]
			if v := b.Get([]byte(scope)); v != nil {
				id = string(v)
			}
		}
		return get(tx, "upstreams", id, &u)
	})
	return u, err
}
func (e *Engine) DeleteUpstream(id string) error {
	if id == "npmjs" {
		return ErrConflict
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.Store.DB.Update(func(tx *bolt.Tx) error {
		if tx.Bucket([]byte("upstreams")).Get([]byte(id)) == nil {
			return ErrNotFound
		}
		err := tx.Bucket([]byte("routes")).ForEach(func(k, v []byte) error {
			if string(v) == id {
				return ErrConflict
			}
			return nil
		})
		if err != nil {
			return err
		}
		// Artifacts keep their source identity. Do not silently reroute old URLs.
		return tx.Bucket([]byte("upstreams")).Delete([]byte(id))
	})
}
func (e *Engine) request(ctx context.Context, u Upstream, raw string) (*http.Request, error) {
	target, err := validateTarget(raw)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", target.String(), nil)
	if err != nil {
		return nil, err
	}
	base, _ := url.Parse(u.URL)
	if sameOrigin(target, base) && u.Token != "" {
		req.Header.Set("Authorization", "Bearer "+u.Token)
	}
	return req, nil
}
