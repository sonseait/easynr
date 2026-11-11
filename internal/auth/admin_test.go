package auth

import (
	"bytes"
	"easynr/internal/state"
	bolt "go.etcd.io/bbolt"
	"strings"
	"sync"
	"testing"
)

func TestBootstrapOnce(t *testing.T) {
	dir := t.TempDir()
	s, err := state.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	if err = EnsureBootstrap(s, &logs); err != nil {
		t.Fatal(err)
	}
	if strings.Count(logs.String(), "bootstrap_admin_password") != 1 {
		t.Fatal("missing bootstrap event")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = state.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = EnsureBootstrap(s, &logs); err != nil {
		t.Fatal(err)
	}
	if strings.Count(logs.String(), "bootstrap_admin_password") != 1 {
		t.Fatal("bootstrap repeated")
	}
}

func TestBootstrapConcurrentAndCorrupt(t *testing.T) {
	s, e := state.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	var log bytes.Buffer
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- EnsureBootstrap(s, &log) }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if strings.Count(log.String(), "bootstrap_admin_password") != 1 {
		t.Fatal("bootstrap count")
	}
	s.DB.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("settings")).Put([]byte("schema_version"), []byte("999"))
	})
	var out bytes.Buffer
	if e = EnsureBootstrap(s, &out); e == nil || out.Len() != 0 {
		t.Fatal("corrupt state bootstrapped")
	}
}
func TestAdminReset(t *testing.T) {
	s, p := service(t)
	secret, _, e := s.Login(p)
	if e != nil {
		t.Fatal(e)
	}
	_, token, e := s.CreateToken("ci", []Grant{{"*", "read"}})
	if e != nil {
		t.Fatal(e)
	}
	var out bytes.Buffer
	if e = ResetPassword(s.Store, &out); e != nil {
		t.Fatal(e)
	}
	newpass := strings.TrimSpace(strings.Split(out.String(), "password=")[1])
	if newpass == p {
		t.Fatal("password unchanged")
	}
	if _, _, e = s.Login(p); e == nil {
		t.Fatal("old password accepted")
	}
	if _, _, e = s.Login(newpass); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Session(secret); e == nil {
		t.Fatal("old session accepted")
	}
	if _, e = s.Authenticate(token); e != nil {
		t.Fatal("docker token revoked by reset")
	}
}

func TestBootstrapMissingAllAdminSettingsFailsClosed(t *testing.T) {
	s, _ := service(t)
	if e := s.Store.DB.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("settings"))
		for _, k := range []string{"bootstrapped", "admin_hash", "schema_version"} {
			if e := b.Delete([]byte(k)); e != nil {
				return e
			}
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	var out bytes.Buffer
	if e := EnsureBootstrap(s.Store, &out); e == nil || out.Len() != 0 {
		t.Fatal("lost admin settings treated as fresh deployment")
	}
}

func TestBootstrapClosedDBEmitsNoSecret(t *testing.T) {
	st, e := state.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	st.Close()
	var out bytes.Buffer
	if e = EnsureBootstrap(st, &out); e == nil || out.Len() != 0 {
		t.Fatal("failed bootstrap emitted a secret")
	}
}
