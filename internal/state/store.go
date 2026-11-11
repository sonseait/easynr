package state

import (
	"errors"
	"fmt"
	bolt "go.etcd.io/bbolt"
	"os"
	"path/filepath"
	"time"
)

type Store struct{ DB *bolt.DB }

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(dir, "metadata.db")
	_, statErr := os.Stat(dbPath)
	fresh := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !fresh {
		return nil, statErr
	}
	db, err := bolt.Open(dbPath, 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		if errors.Is(err, bolt.ErrTimeout) {
			return nil, fmt.Errorf("data directory is in use: %w", err)
		}
		return nil, err
	}
	buckets := []string{"settings", "tokens", "sessions", "packages", "cache", "artifacts", "upstreams", "routes"}
	if fresh {
		err = db.Update(func(tx *bolt.Tx) error {
			for _, name := range buckets {
				if _, err := tx.CreateBucket([]byte(name)); err != nil {
					return err
				}
			}
			return tx.Bucket([]byte("settings")).Put([]byte("bootstrap_pending"), []byte("1"))
		})
	} else {
		err = db.View(func(tx *bolt.Tx) error {
			for _, name := range buckets {
				if tx.Bucket([]byte(name)) == nil {
					return fmt.Errorf("invalid metadata: missing %s bucket", name)
				}
			}
			return nil
		})
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{DB: db}, nil
}
func (s *Store) Close() error { return s.DB.Close() }
