package registry

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	bolt "go.etcd.io/bbolt"
)

var cacheCompressors = sync.Pool{New: func() any {
	w, _ := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed)
	return w
}}

// Large full packuments otherwise keep megabytes of documentation resident in
// bbolt's memory mapping. Small values retain the original plain JSON encoding.
func compressCache(raw []byte) ([]byte, error) {
	if len(raw) < 4096 {
		return raw, nil
	}
	var buf bytes.Buffer
	w := cacheCompressors.Get().(*gzip.Writer)
	w.Reset(&buf)
	defer func() { w.Reset(io.Discard); cacheCompressors.Put(w) }()
	if _, err := w.Write(raw); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	if buf.Len() >= len(raw) {
		return raw, nil
	}
	return buf.Bytes(), nil
}

func decodeCache(raw []byte, out any) error {
	if !bytes.HasPrefix(raw, []byte{0x1f, 0x8b}) {
		return json.Unmarshal(raw, out) // Cache created by earlier releases.
	}
	r, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	defer r.Close()
	// Rewritten URLs and the cache envelope can exceed the upstream JSON limit.
	d := json.NewDecoder(io.LimitReader(r, 4*maxMetadata+(1<<20)))
	if err := d.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("invalid compressed metadata cache: %v", err)
	}
	return nil
}

// Cache keys are upstream:name or upstream:install:name. Neither upstream IDs
// nor npm package names contain colons, so the name needs no metadata decoding.
func cachePackageName(key []byte) string {
	s := string(key)
	return s[strings.LastIndexByte(s, ':')+1:]
}

type CacheStats struct {
	Packages int   `json:"packages"`
	Bytes    int64 `json:"bytes"`
}

// CacheStatistics includes every upstream and counts shared tarball content once.
// Metadata-only packages count as cached, but contribute no tarball bytes.
func (e *Engine) CacheStatistics() (CacheStats, error) {
	stats := CacheStats{}
	packages, hashes := map[string]bool{}, map[string]bool{}
	err := e.Store.DB.View(func(tx *bolt.Tx) error {
		if err := tx.Bucket([]byte("cache")).ForEach(func(k, _ []byte) error {
			packages[cachePackageName(k)] = true
			return nil
		}); err != nil {
			return err
		}
		return tx.Bucket([]byte("artifacts")).ForEach(func(_, v []byte) error {
			var a Artifact
			if err := json.Unmarshal(v, &a); err != nil {
				return err
			}
			if a.Source == "local" || a.Hash == "" {
				return nil
			}
			packages[a.Package] = true
			if !hashes[a.Hash] {
				hashes[a.Hash] = true
				stats.Bytes += a.Size
			}
			return nil
		})
	})
	stats.Packages = len(packages)
	return stats, err
}
