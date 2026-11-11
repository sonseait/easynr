package state

import (
	bolt "go.etcd.io/bbolt"
	"strings"
	"testing"
)

func TestStoreReopen(t *testing.T) {
	d := t.TempDir()
	s, err := Open(d)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.DB.Update(func(tx *bolt.Tx) error { return tx.Bucket([]byte("settings")).Put([]byte("test"), []byte("value")) }); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(d)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.DB.View(func(tx *bolt.Tx) error {
		if string(tx.Bucket([]byte("settings")).Get([]byte("test"))) != "value" {
			t.Error("value lost")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestStoreExclusiveAndCorrupt(t *testing.T) {
	dir := t.TempDir()
	s, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	if second, e := Open(dir); e == nil {
		second.Close()
		t.Fatal("concurrent open accepted")
	} else if !strings.Contains(e.Error(), "data directory is in use") {
		t.Fatal(e)
	}
	if e = s.DB.Update(func(tx *bolt.Tx) error { return tx.DeleteBucket([]byte("settings")) }); e != nil {
		t.Fatal(e)
	}
	s.Close()
	if reopened, e := Open(dir); e == nil {
		reopened.Close()
		t.Fatal("corrupt buckets recreated")
	}
}
