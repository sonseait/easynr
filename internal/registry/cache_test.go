package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"
)

func TestCacheStatistics(t *testing.T) {
	e := engine(t)
	check := func(packages int, size int64) {
		t.Helper()
		stats, err := e.CacheStatistics()
		if err != nil || stats.Packages != packages || stats.Bytes != size {
			t.Fatalf("cache stats = %+v, err=%v; want packages=%d bytes=%d", stats, err, packages, size)
		}
	}
	check(0, 0)
	if err := e.Publish("local-package", payload("local-package", "1.0.0", []byte("local data"))); err != nil {
		t.Fatal(err)
	}
	check(0, 0)
	first := remoteFixture(t, "@one/pkg")
	useRemote(t, e, first, "first", "@one")
	doc, err := e.Metadata(context.Background(), first.Name, false)
	if err != nil {
		t.Fatal(err)
	}
	check(1, 0) // Cached metadata counts even before a tarball has been downloaded.
	if _, err := e.Blob(context.Background(), idOf(object(doc["versions"])["1.0.0"])); err != nil {
		t.Fatal(err)
	}
	check(1, int64(len(first.Data)))
	if _, err := e.Blob(context.Background(), idOf(object(doc["versions"])["2.0.0"])); err != nil {
		t.Fatal(err)
	}
	check(1, int64(len(first.Data))) // Shared versions use the same physical blob.
	second := remoteFixture(t, "@two/pkg")
	useRemote(t, e, second, "second", "@two")
	doc, err = e.Metadata(context.Background(), second.Name, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Blob(context.Background(), idOf(object(doc["versions"])["1.0.0"])); err != nil {
		t.Fatal(err)
	}
	check(2, int64(len(first.Data))) // Deduplicate across packages and upstreams too.
	if err := e.SaveRoute(Route{Scope: "@one", Upstream: "npmjs"}); err != nil {
		t.Fatal(err)
	}
	check(2, int64(len(first.Data))) // Old upstream caches still occupy cache storage.
	if err := e.ClearCache(first.Name); err != nil {
		t.Fatal(err)
	}
	check(1, int64(len(first.Data)))
	if err := e.ClearCache(""); err != nil {
		t.Fatal(err)
	}
	check(0, 0)
	if _, err := e.local("local-package"); err != nil {
		t.Fatal("cache clear removed local package", err)
	}
}

func TestCompressedMetadataCacheAndLegacyJSON(t *testing.T) {
	e := engine(t)
	f := remoteFixture(t, "pkg")
	useRemote(t, e, f, "mock", "*")
	doc, err := e.Metadata(context.Background(), "pkg", false)
	if err != nil {
		t.Fatal(err)
	}
	doc["readme"] = strings.Repeat("documentation ", 1000)
	cached := Cached{Upstream: "mock", Name: "pkg", Document: doc, FetchedAt: e.Now()}
	legacy, err := json.Marshal(cached)
	if err != nil {
		t.Fatal(err)
	}
	// Emulate an existing data directory without rewriting it on startup.
	if err := e.Store.DB.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("cache")).Put([]byte("mock:pkg"), legacy)
	}); err != nil {
		t.Fatal(err)
	}
	if result, err := e.Metadata(context.Background(), "pkg", false); err != nil || result["readme"] != doc["readme"] {
		t.Fatal("legacy JSON unreadable", err)
	}
	if err := e.write("cache", "mock:pkg", cached); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.DB.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket([]byte("cache")).Get([]byte("mock:pkg"))
		if !bytes.HasPrefix(raw, []byte{0x1f, 0x8b}) || len(raw) >= len(legacy)/2 {
			t.Error("large packument not compressed", len(raw), len(legacy))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if result, err := e.Metadata(context.Background(), "pkg", false); err != nil || result["readme"] != doc["readme"] {
		t.Fatal("compressed metadata changed", err)
	}
	if err := e.Publish("pkg", payload("pkg", "0.1.0", []byte("local"))); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Blob(context.Background(), idOf(object(doc["versions"])["1.0.0"])); err != nil {
		t.Fatal(err)
	}
	if list, err := e.Packages(); err != nil || len(list) != 1 || list[0].CachedVersions != 2 {
		t.Fatal("compressed cache not listed", list, err)
	}
	if stats, err := e.CacheStatistics(); err != nil || stats.Packages != 1 || stats.Bytes != int64(len(f.Data)) {
		t.Fatal("compressed stats incorrect", stats, err)
	}
	if result, err := e.GC(true); err != nil || result.Blobs != 0 || result.Artifacts != 0 {
		t.Fatal("GC lost compressed cache references", result, err)
	}
	if err := e.ClearCache("pkg"); err != nil {
		t.Fatal(err)
	}
	if stats, err := e.CacheStatistics(); err != nil || stats.Packages != 0 || stats.Bytes != 0 {
		t.Fatal("clear missed compressed cache", stats, err)
	}
}

func TestCompressedCacheRejectsCorruption(t *testing.T) {
	raw := []byte(`{"readme":"` + strings.Repeat("documentation ", 1000) + `"}`)
	compressed, err := compressCache(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{compressed[:len(compressed)/2], append(append([]byte{}, compressed[:len(compressed)-1]...), compressed[len(compressed)-1]^0xff)} {
		var doc Document
		if err := decodeCache(data, &doc); err == nil {
			t.Fatal("corrupt compressed cache accepted")
		}
	}
}
