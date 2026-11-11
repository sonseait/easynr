package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"easynr/internal/state"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	bolt "go.etcd.io/bbolt"
	"golang.org/x/crypto/bcrypt"
	"regexp"
	"strings"
	"time"
)

var ErrUnauthorized = errors.New("unauthorized")
var ErrNotFound = errors.New("not found")
var ErrInvalid = errors.New("invalid input")

type Grant struct {
	Repository string `json:"package"`
	Permission string `json:"permission"`
}
type TokenRecord struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Grants    []Grant   `json:"grants"`
	CreatedAt time.Time `json:"createdAt"`
}
type storedToken struct {
	TokenRecord
	Hash []byte `json:"hash"`
}
type Service struct {
	Store *state.Store
	Now   func() time.Time
}
type Session struct {
	CSRF    string
	Expires time.Time
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
func random(n int) (string, error) {
	b := make([]byte, n)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func key(secret string) []byte { h := sha256.Sum256([]byte(secret)); return h[:] }

var packagePart = regexp.MustCompile(`^[a-z0-9][a-z0-9._~-]*$`)

func ValidPackage(name string) bool {
	if len(name) == 0 || len(name) > 214 {
		return false
	}
	if strings.HasPrefix(name, "@") {
		parts := strings.Split(name[1:], "/")
		return len(parts) == 2 && packagePart.MatchString(parts[0]) && packagePart.MatchString(parts[1])
	}
	return packagePart.MatchString(name)
}
func ValidRepository(name string) bool { return ValidPackage(name) }
func validGrant(name string) bool {
	return name == "*" || ValidPackage(name) || (strings.HasPrefix(name, "@") && strings.HasSuffix(name, "/*") && packagePart.MatchString(strings.TrimSuffix(name[1:], "/*")))
}

func (s Service) Login(password string) (string, Session, error) {
	var hash []byte
	e := s.Store.DB.View(func(tx *bolt.Tx) error {
		hash = append([]byte(nil), tx.Bucket([]byte("settings")).Get([]byte("admin_hash"))...)
		return nil
	})
	if e != nil {
		return "", Session{}, e
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(password)) != nil {
		return "", Session{}, ErrUnauthorized
	}
	secret, e := random(32)
	if e != nil {
		return "", Session{}, e
	}
	session := Session{CSRF: hex.EncodeToString(key("csrf:" + secret)), Expires: s.now().Add(12 * time.Hour)}
	e = s.Store.DB.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("sessions")) // prune expired records on login to bound persistent session storage
		c := b.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var old Session
			if e := json.Unmarshal(v, &old); e != nil {
				return e
			}
			if !s.now().Before(old.Expires) {
				if e := c.Delete(); e != nil {
					return e
				}
			}
		}
		data, e := json.Marshal(session)
		if e != nil {
			return e
		}
		return b.Put(key(secret), data)
	})
	return secret, session, e
}
func (s Service) Session(secret string) (Session, error) {
	var session Session
	if secret == "" {
		return session, ErrUnauthorized
	}
	e := s.Store.DB.View(func(tx *bolt.Tx) error {
		v := tx.Bucket([]byte("sessions")).Get(key(secret))
		if v == nil {
			return ErrUnauthorized
		}
		return json.Unmarshal(v, &session)
	})
	if e != nil {
		return Session{}, e
	}
	if len(session.CSRF) != 64 || subtle.ConstantTimeCompare([]byte(session.CSRF), []byte(hex.EncodeToString(key("csrf:"+secret)))) != 1 || !s.now().Before(session.Expires) {
		return Session{}, ErrUnauthorized
	}
	return session, nil
}
func (s Service) Logout(secret string) error {
	return s.Store.DB.Update(func(tx *bolt.Tx) error { return tx.Bucket([]byte("sessions")).Delete(key(secret)) })
}
func (s Service) CreateToken(name string, grants []Grant) (TokenRecord, string, error) {
	if strings.TrimSpace(name) == "" || len(name) > 200 || len(grants) == 0 || len(grants) > 100 {
		return TokenRecord{}, "", fmt.Errorf("%w: name and grants required (name <=200 bytes, 1–100 grants)", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, g := range grants {
		if !validGrant(g.Repository) || (g.Permission != "read" && g.Permission != "write") || seen[g.Repository] {
			return TokenRecord{}, "", fmt.Errorf("%w: invalid or duplicate grant", ErrInvalid)
		}
		seen[g.Repository] = true
	}
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return TokenRecord{}, "", e
	}
	id := hex.EncodeToString(b)
	secret, e := random(32)
	if e != nil {
		return TokenRecord{}, "", e
	}
	credential := "enr_" + id + "." + secret
	rec := TokenRecord{ID: id, Name: name, Grants: grants, CreatedAt: s.now().UTC()}
	data, e := json.Marshal(storedToken{TokenRecord: rec, Hash: key(credential)})
	if e != nil {
		return TokenRecord{}, "", e
	}
	e = s.Store.DB.Update(func(tx *bolt.Tx) error { return tx.Bucket([]byte("tokens")).Put([]byte(id), data) })
	return rec, credential, e
}
func (s Service) Tokens() ([]TokenRecord, error) {
	result := []TokenRecord{}
	e := s.Store.DB.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("tokens")).ForEach(func(k, v []byte) error {
			var rec storedToken
			if e := json.Unmarshal(v, &rec); e != nil {
				return e
			}
			result = append(result, rec.TokenRecord)
			return nil
		})
	})
	return result, e
}
func (s Service) Revoke(id string) error {
	return s.Store.DB.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("tokens"))
		if b.Get([]byte(id)) == nil {
			return ErrNotFound
		}
		return b.Delete([]byte(id))
	})
}
func (s Service) Authenticate(credential string) (TokenRecord, error) {
	if len(credential) != 80 || !strings.HasPrefix(credential, "enr_") || credential[36] != '.' {
		return TokenRecord{}, ErrUnauthorized
	}
	id := credential[4:36]
	var rec storedToken
	e := s.Store.DB.View(func(tx *bolt.Tx) error {
		v := tx.Bucket([]byte("tokens")).Get([]byte(id))
		if v == nil {
			return ErrUnauthorized
		}
		return json.Unmarshal(v, &rec)
	})
	if e != nil {
		return TokenRecord{}, e
	}
	if subtle.ConstantTimeCompare(rec.Hash, key(credential)) != 1 {
		return TokenRecord{}, ErrUnauthorized
	}
	return rec.TokenRecord, nil
}

// Allows uses npm operations and exact package, scope wildcard, or global grants.
func Allows(grants []Grant, name, action string) bool {
	if action != "read" && action != "write" {
		return false
	}
	for _, g := range grants {
		match := g.Repository == "*" || g.Repository == name || (strings.HasPrefix(g.Repository, "@") && strings.HasSuffix(g.Repository, "/*") && strings.HasPrefix(name, strings.TrimSuffix(g.Repository, "*")))
		if match && (g.Permission == "write" || g.Permission == "read" && action == "read") {
			return true
		}
	}
	return false
}
