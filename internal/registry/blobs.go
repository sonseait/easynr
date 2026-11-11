package registry

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"

	bolt "go.etcd.io/bbolt"
)

type checksums struct {
	sha256 hash.Hash
	sums   map[string]hash.Hash
}

func integrityAlgorithm(integrity string) string {
	ranks := map[string]int{"sha1": 1, "sha256": 2, "sha384": 3, "sha512": 4}
	strongest := ""
	for _, token := range strings.Fields(integrity) {
		algorithm, _, ok := strings.Cut(token, "-")
		if ok && ranks[algorithm] > ranks[strongest] {
			strongest = algorithm
		}
	}
	return strongest
}

func newChecksums(integrity, shasum string) *checksums {
	c := &checksums{sha256: sha256.New(), sums: map[string]hash.Hash{}}
	c.sums["sha256"] = c.sha256 // Content address is always SHA-256.
	switch integrityAlgorithm(integrity) {
	case "sha1":
		c.sums["sha1"] = sha1.New()
	case "sha384":
		c.sums["sha384"] = sha512.New384()
	case "sha512":
		c.sums["sha512"] = sha512.New()
	}
	if shasum != "" && c.sums["sha1"] == nil {
		c.sums["sha1"] = sha1.New()
	}
	return c
}
func (c *checksums) writer() io.Writer {
	writers := make([]io.Writer, 0, len(c.sums))
	for _, sum := range c.sums {
		writers = append(writers, sum)
	}
	return io.MultiWriter(writers...)
}
func (c *checksums) verify(integrity, shasum string) error {
	if integrity != "" {
		// SRI chooses the strongest supported algorithm, accepting any matching digest of that strength.
		strongest := integrityAlgorithm(integrity)
		matched := false
		for _, token := range strings.Fields(integrity) {
			parts := strings.SplitN(token, "-", 2)
			if len(parts) != 2 {
				continue
			}
			if strongest == "" || parts[0] != strongest {
				continue
			}
			expected, err := base64.StdEncoding.DecodeString(strings.SplitN(parts[1], "?", 2)[0])
			if err == nil && bytes.Equal(expected, c.sums[strongest].Sum(nil)) {
				matched = true
			}
		}
		if !matched {
			return errors.New("integrity mismatch")
		}
	}
	if shasum != "" && !strings.EqualFold(hex.EncodeToString(c.sums["sha1"].Sum(nil)), shasum) {
		return errors.New("shasum mismatch")
	}
	return nil
}
func verifyBytes(data []byte, integrity, shasum string) error {
	c := newChecksums(integrity, shasum)
	_, _ = c.writer().Write(data)
	return c.verify(integrity, shasum)
}
func (e *Engine) blobPath(h string) string { return filepath.Join(e.Dir, "blobs", h) }
func (e *Engine) storeBytes(data []byte) (string, error) {
	h, _, err := e.storeStream(bytes.NewReader(data), "", "")
	return h, err
}
func (e *Engine) storeStream(r io.Reader, integrity, shasum string) (string, int64, error) {
	f, err := os.CreateTemp(filepath.Join(e.Dir, "tmp"), "tarball-")
	if err != nil {
		return "", 0, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	sums := newChecksums(integrity, shasum)
	n, err := io.Copy(io.MultiWriter(f, sums.writer()), io.LimitReader(r, maxTarball+1))
	if err != nil {
		return "", 0, err
	}
	if n > maxTarball {
		return "", 0, errors.New("tarball exceeds 128 MiB")
	}
	if err = sums.verify(integrity, shasum); err != nil {
		return "", 0, err
	}
	digest := hex.EncodeToString(sums.sha256.Sum(nil))
	if err = f.Sync(); err != nil {
		return "", 0, err
	}
	if err = f.Close(); err != nil {
		return "", 0, err
	}
	if err = os.Rename(f.Name(), e.blobPath(digest)); err != nil {
		return "", 0, err
	}
	return digest, n, nil
}
func (e *Engine) Blob(ctx context.Context, id string) (Artifact, error) {
	var a Artifact
	if err := e.read("artifacts", id, &a); err != nil {
		return a, err
	}
	if a.Hash != "" {
		if _, err := os.Stat(e.blobPath(a.Hash)); err == nil {
			return a, nil
		}
	}
	if a.Source == "local" {
		return a, ErrNotFound
	}
	result, err, _ := e.flights.Do("blob:"+id, func() (any, error) {
		var current Artifact
		if err := e.read("artifacts", id, &current); err != nil {
			return nil, err
		}
		if current.Hash != "" {
			if _, err := os.Stat(e.blobPath(current.Hash)); err == nil {
				return current, nil
			}
		}
		select {
		case e.downloadSlots <- struct{}{}:
			defer func() { <-e.downloadSlots }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		var u Upstream
		if err := e.read("upstreams", current.Source, &u); err != nil {
			return nil, ErrUpstream
		}
		req, err := e.request(ctx, u, current.URL)
		if err != nil {
			return nil, ErrUpstream
		}
		resp, err := e.Client.Do(req)
		if err != nil {
			return nil, ErrUpstream
		}
		defer resp.Body.Close()
		if resp.StatusCode == 404 {
			return nil, ErrNotFound
		}
		if resp.StatusCode != 200 {
			return nil, ErrUpstream
		}
		current.Hash, current.Size, err = e.storeStream(resp.Body, current.Integrity, current.Shasum)
		if err != nil {
			return nil, fmt.Errorf("%w: download failed integrity or size checks", ErrUpstream)
		}
		e.mu.RLock()
		defer e.mu.RUnlock()
		err = e.Store.DB.Update(func(tx *bolt.Tx) error {
			if tx.Bucket([]byte("artifacts")).Get([]byte(id)) == nil {
				return ErrNotFound
			}
			return put(tx, "artifacts", id, current)
		})
		return current, err
	})
	if err != nil {
		return a, err
	}
	return result.(Artifact), nil
}

type GCResult struct {
	DryRun         bool  `json:"dryRun"`
	Blobs          int   `json:"blobs"`
	Bytes          int64 `json:"bytes"`
	Artifacts      int   `json:"artifacts"`
	TemporaryFiles int   `json:"temporaryFiles"`
}

// GC must be run with the exclusive process-level bbolt lock, while the server is stopped.
func (e *Engine) GC(dry bool) (GCResult, error) {
	result := GCResult{DryRun: dry}
	liveIDs := map[string]bool{}
	liveHashes := map[string]bool{}
	deadIDs := []string{}
	mark := func(versions Document) {
		for _, v := range versions {
			path := str(object(object(v)["dist"])["tarball"])
			id := strings.TrimSuffix(strings.TrimPrefix(path, "/-/tarballs/"), ".tgz")
			if len(id) == 64 {
				liveIDs[id] = true
			}
		}
	}
	err := e.Store.DB.View(func(tx *bolt.Tx) error {
		if err := tx.Bucket([]byte("packages")).ForEach(func(k, v []byte) error {
			var p LocalPackage
			if err := json.Unmarshal(v, &p); err != nil {
				return err
			}
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
			var c Cached
			if err := decodeCache(v, &c); err != nil {
				return err
			}
			mark(object(c.Document["versions"]))
			return nil
		}); err != nil {
			return err
		}
		return tx.Bucket([]byte("artifacts")).ForEach(func(k, v []byte) error {
			var a Artifact
			if err := json.Unmarshal(v, &a); err != nil {
				return err
			}
			if liveIDs[string(k)] {
				if a.Hash != "" {
					liveHashes[a.Hash] = true
				}
			} else {
				deadIDs = append(deadIDs, string(k))
			}
			return nil
		})
	})
	if err != nil {
		return result, err
	}
	result.Artifacts = len(deadIDs)
	if !dry {
		if err = e.Store.DB.Update(func(tx *bolt.Tx) error {
			for _, id := range deadIDs {
				if err := tx.Bucket([]byte("artifacts")).Delete([]byte(id)); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return result, err
		}
	}
	for _, sub := range []string{"blobs", "tmp"} {
		entries, err := os.ReadDir(filepath.Join(e.Dir, sub))
		if err != nil {
			return result, err
		}
		for _, entry := range entries {
			if entry.IsDir() || sub == "blobs" && liveHashes[entry.Name()] {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return result, err
			}
			if sub == "blobs" {
				result.Blobs++
			} else {
				result.TemporaryFiles++
			}
			result.Bytes += info.Size()
			if !dry {
				if err := os.Remove(filepath.Join(e.Dir, sub, entry.Name())); err != nil {
					return result, err
				}
			}
		}
	}
	return result, nil
}
