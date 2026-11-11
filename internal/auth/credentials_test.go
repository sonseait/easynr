package auth

import (
	"bytes"
	"easynr/internal/state"
	"encoding/json"
	bolt "go.etcd.io/bbolt"
	"strings"
	"testing"
	"time"
)

func service(t *testing.T) (Service, string) {
	t.Helper()
	st, e := state.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { st.Close() })
	var log bytes.Buffer
	if e = EnsureBootstrap(st, &log); e != nil {
		t.Fatal(e)
	}
	return Service{Store: st}, strings.TrimSpace(strings.Split(log.String(), "password=")[1])
}
func TestSession(t *testing.T) {
	s, p := service(t)
	now := time.Now()
	s.Now = func() time.Time { return now }
	if _, _, e := s.Login("wrong"); e == nil {
		t.Fatal("wrong password accepted")
	}
	secret, session, e := s.Login(p)
	if e != nil {
		t.Fatal("valid password rejected:", e)
	}
	if secret == "" || session.CSRF == "" {
		t.Fatal("missing session secrets")
	}
	if _, e = s.Session(secret); e != nil {
		t.Fatal(e)
	}
	now = now.Add(12 * time.Hour)
	if _, e = s.Session(secret); e == nil {
		t.Fatal("expired session accepted")
	}
	now = now.Add(-time.Hour)
	if e = s.Logout(secret); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Session(secret); e == nil {
		t.Fatal("logged out session accepted")
	}
}
func TestToken(t *testing.T) {
	s, _ := service(t)
	rec, secret, e := s.CreateToken("ci", []Grant{{"@team/app", "write"}})
	if e != nil {
		t.Fatal(e)
	}
	if !strings.HasPrefix(secret, "enr_"+rec.ID+".") {
		t.Fatal("invalid credential format")
	}
	if _, e = s.Authenticate(secret); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Authenticate(secret + "x"); e == nil {
		t.Fatal("wrong secret accepted")
	}
	records, e := s.Tokens()
	if e != nil || len(records) != 1 {
		t.Fatal("list failed", e)
	}
	b, _ := json.Marshal(records)
	if strings.Contains(string(b), secret) || strings.Contains(string(b), "hash") {
		t.Fatal("secret leaked")
	}
	if e = s.Revoke(rec.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Authenticate(secret); e == nil {
		t.Fatal("revoked credential accepted")
	}
}
func TestTokenValidation(t *testing.T) {
	s, _ := service(t)
	for _, g := range [][]Grant{nil, {{"", "read"}}, {{"Team/app", "read"}}, {{"team/app:tag", "write"}}, {{"a", "delete"}}, {{"a", "read"}, {"a", "write"}}} {
		if _, _, e := s.CreateToken("ci", g); e == nil {
			t.Errorf("accepted invalid grants %v", g)
		}
	}
}
func TestAllowsExactRepository(t *testing.T) {
	grants := []Grant{{"@team/app", "read"}}
	for _, tc := range []struct {
		repo, action string
		want         bool
	}{{"@team/app", "read", true}, {"@team/app", "write", false}, {"@team/app2", "read", false}, {"@team/app/sub", "read", false}, {"@team/app", "delete", false}} {
		if got := Allows(grants, tc.repo, tc.action); got != tc.want {
			t.Errorf("Allows(%q,%q)=%v want %v", tc.repo, tc.action, got, tc.want)
		}
	}
	if !Allows([]Grant{{"*", "write"}}, "@any/repo", "write") {
		t.Error("global write denied")
	}
}

func TestCredentialPersistenceAndHashOnlyStorage(t *testing.T) {
	dir := t.TempDir()
	st, e := state.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	var out bytes.Buffer
	if e = EnsureBootstrap(st, &out); e != nil {
		t.Fatal(e)
	}
	password := strings.TrimSpace(strings.Split(out.String(), "password=")[1])
	s := Service{Store: st}
	cookie, _, e := s.Login(password)
	if e != nil {
		t.Fatal(e)
	}
	_, token, e := s.CreateToken("ci", []Grant{{"@team/app", "read"}})
	if e != nil {
		t.Fatal(e)
	}
	if e = st.DB.View(func(tx *bolt.Tx) error {
		for _, name := range []string{"settings", "tokens", "sessions"} {
			if e := tx.Bucket([]byte(name)).ForEach(func(k, v []byte) error {
				for _, secret := range []string{password, cookie, token} {
					if bytes.Contains(k, []byte(secret)) || bytes.Contains(v, []byte(secret)) {
						t.Error("plaintext credential persisted")
					}
				}
				return nil
			}); e != nil {
				return e
			}
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	st.Close()
	st, e = state.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	s = Service{Store: st}
	if _, _, e = s.Login(password); e != nil {
		t.Fatal("password not persistent", e)
	}
	if _, e = s.Session(cookie); e != nil {
		t.Fatal("session not persistent", e)
	}
	if _, e = s.Authenticate(token); e != nil {
		t.Fatal("token not persistent", e)
	}
}
func TestSessionCorruptRecordRejected(t *testing.T) {
	s, p := service(t)
	secret, _, e := s.Login(p)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Store.DB.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("sessions")).Put(key(secret), []byte(`{"Expires":"2099-01-01T00:00:00Z"}`))
	}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Session(secret); e == nil {
		t.Fatal("schema-invalid session accepted")
	}
}
