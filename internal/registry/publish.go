package registry

import (
	"crypto/sha1"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"easynr/internal/auth"
	"github.com/Masterminds/semver/v3"
	bolt "go.etcd.io/bbolt"
)

const maxTarball = 128 << 20
const maxPublish = 180 << 20

func validVersion(v string) bool { _, err := semver.StrictNewVersion(v); return err == nil }
func validTag(t string) bool {
	if t == "" || len(t) > 128 || strings.Trim(t, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-") != "" {
		return false
	}
	_, err := semver.NewConstraint(t)
	return err != nil
}
func sortPackages(items []PackageInfo) {
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
}
func (e *Engine) Publish(name string, body []byte) error {
	var doc Document
	if !auth.ValidPackage(name) || len(body) > maxPublish || json.Unmarshal(body, &doc) != nil || str(doc["name"]) != name {
		return ErrInvalid
	}
	versions := object(doc["versions"])
	attachments := object(doc["_attachments"])
	if len(versions) != 1 || len(attachments) != 1 {
		return fmt.Errorf("%w: publish exactly one version and tarball", ErrInvalid)
	}
	var version string
	var item Document
	for v, data := range versions {
		version = v
		item = object(data)
	}
	if !validVersion(version) || str(item["name"]) != name || str(item["version"]) != version {
		return ErrInvalid
	}
	var data []byte
	for _, v := range attachments {
		a := object(v)
		encoded := str(a["data"])
		if len(encoded) > base64.StdEncoding.EncodedLen(maxTarball) {
			return ErrInvalid
		}
		var err error
		data, err = base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(data) == 0 || len(data) > maxTarball {
			return ErrInvalid
		}
	}
	dist := object(item["dist"])
	if err := verifyBytes(data, str(dist["integrity"]), str(dist["shasum"])); err != nil {
		return fmt.Errorf("%w: tarball checksum mismatch", ErrInvalid)
	}
	sha := sha1.Sum(data)
	sri := sha512.Sum512(data)
	a := Artifact{Package: name, Version: version, Source: "local", Integrity: "sha512-" + base64.StdEncoding.EncodeToString(sri[:]), Shasum: hex.EncodeToString(sha[:]), Size: int64(len(data))}
	a.ID = artifactID(a)
	e.mu.Lock()
	defer e.mu.Unlock()
	var p LocalPackage
	err := e.read("packages", name, &p)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if p.Versions != nil && p.Versions[version] != nil {
		return ErrConflict
	}
	tags := object(doc["dist-tags"])
	for t, v := range tags {
		if !validTag(t) || str(v) != version {
			return fmt.Errorf("%w: tag must name the published version", ErrInvalid)
		}
	}
	a.Hash, err = e.storeBytes(data)
	if err != nil {
		return err
	}
	dist["tarball"] = artifactPath(a.ID)
	dist["integrity"] = a.Integrity
	dist["shasum"] = a.Shasum
	item["dist"] = dist
	if p.Versions == nil {
		p = LocalPackage{Name: name, Versions: map[string]Document{}, Tags: map[string]string{}}
	}
	p.Versions[version] = item
	for t, v := range tags {
		p.Tags[t] = str(v)
	}
	p.UpdatedAt = e.Now()
	return e.Store.DB.Update(func(tx *bolt.Tx) error {
		if err := put(tx, "artifacts", a.ID, a); err != nil {
			return err
		}
		return put(tx, "packages", name, p)
	})
}
func (e *Engine) SetTag(name, tag, version string, remove bool) error {
	if !validTag(tag) {
		return ErrInvalid
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.Store.DB.Update(func(tx *bolt.Tx) error {
		var p LocalPackage
		err := get(tx, "packages", name, &p)
		if err != nil {
			return err
		}
		if remove {
			if _, ok := p.Tags[tag]; !ok {
				return ErrNotFound
			}
			delete(p.Tags, tag)
		} else {
			if _, ok := p.Versions[version]; !ok {
				return fmt.Errorf("%w: tag updates require a local version", ErrNotFound)
			}
			p.Tags[tag] = version
		}
		p.UpdatedAt = e.Now()
		return put(tx, "packages", name, p)
	})
}
