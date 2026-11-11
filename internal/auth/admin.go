package auth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"

	"easynr/internal/state"
	bolt "go.etcd.io/bbolt"
	"golang.org/x/crypto/bcrypt"
)

func EnsureBootstrap(s *state.Store, out io.Writer) error {
	var password string
	err := s.DB.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("settings"))
		if b == nil {
			return errors.New("settings bucket missing")
		}
		flag, hash, schema := b.Get([]byte("bootstrapped")), b.Get([]byte("admin_hash")), b.Get([]byte("schema_version"))
		if flag != nil || hash != nil || schema != nil {
			if string(flag) != "1" || len(hash) == 0 || string(schema) != "1" {
				return errors.New("invalid bootstrap state")
			}
			if _, err := bcrypt.Cost(hash); err != nil {
				return fmt.Errorf("invalid admin hash: %w", err)
			}
			return nil
		}
		if string(b.Get([]byte("bootstrap_pending"))) != "1" {
			return errors.New("invalid bootstrap state: no first-start marker")
		}
		random := make([]byte, 24)
		if _, err := rand.Read(random); err != nil {
			return err
		}
		password = base64.RawURLEncoding.EncodeToString(random)
		generated, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		for _, pair := range [][2][]byte{{[]byte("admin_hash"), generated}, {[]byte("schema_version"), []byte("1")}, {[]byte("bootstrapped"), []byte("1")}} {
			if err := b.Put(pair[0], pair[1]); err != nil {
				return err
			}
		}
		return b.Delete([]byte("bootstrap_pending"))
	})
	if err != nil {
		return err
	}
	if password != "" {
		_, err = fmt.Fprintf(out, "bootstrap_admin_password password=%s\n", password)
	}
	return err
}

func ResetPassword(s *state.Store, out io.Writer) error {
	password, e := random(24)
	if e != nil {
		return e
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if e != nil {
		return e
	}
	e = s.DB.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("settings"))
		if string(b.Get([]byte("schema_version"))) != "1" || string(b.Get([]byte("bootstrapped"))) != "1" {
			return errors.New("admin is not bootstrapped or state is invalid")
		}
		if e := b.Put([]byte("admin_hash"), hash); e != nil {
			return e
		}
		if e := tx.DeleteBucket([]byte("sessions")); e != nil {
			return e
		}
		_, e := tx.CreateBucket([]byte("sessions"))
		return e
	})
	if e != nil {
		return e
	}
	_, e = fmt.Fprintf(out, "reset_admin_password password=%s\n", password)
	return e
}
