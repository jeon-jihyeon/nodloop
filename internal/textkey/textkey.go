// Package textkey holds the HMAC key of a record directory that every hashed text is kept under
package textkey

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	// The key file in the record directory
	FileName = "hmac.key"
	// Bytes of the key
	size = 32
)

// The secret a short text is hashed under so trying candidates never finds the text again
// It never leaves the machine and no record holds it
type Key struct {
	secret []byte
}

// The key of the record directory
// 1. the first caller creates it by an exclusive create of the name
// 2. a caller that loses that race reads the key the winner wrote so two processes never hash under two keys
// 3. a file of another length fails with ErrInvalid since hashing on under a new key would split old hashes from new
// 4. a file another user may read or write fails with ErrShared and is never repaired or replaced
// Whoever reads the key can try candidates against every hash it keyed so the person decides how it came to be shared
func Load(dir string) (Key, error) {
	f := file(filepath.Join(dir, FileName))
	k, err := f.read()
	if !errors.Is(err, fs.ErrNotExist) {
		return k, err
	}
	if err := f.create(); err != nil && !errors.Is(err, fs.ErrExist) {
		return Key{}, err
	}
	return f.read()
}

// The hex of HMAC-SHA256 of the text under the key
func (k Key) Sum(text string) string {
	mac := hmac.New(sha256.New, k.secret)
	mac.Write([]byte(text))
	return hex.EncodeToString(mac.Sum(nil))
}

// The path of the key file
type file string

func (f file) read() (Key, error) {
	info, err := os.Stat(string(f))
	if err != nil {
		return Key{}, fmt.Errorf("%w: %w", ErrRead, err)
	}
	if shared(info.Mode()) {
		return Key{}, fmt.Errorf("%w: %s has mode %04o, run chmod 600 on it to keep its hashes", ErrShared, f, info.Mode().Perm())
	}
	b, err := os.ReadFile(string(f))
	if err != nil {
		return Key{}, fmt.Errorf("%w: %w", ErrRead, err)
	}
	if len(b) != size {
		return Key{}, fmt.Errorf("%w: %s holds %d bytes", ErrInvalid, f, len(b))
	}
	return Key{secret: b}, nil
}

// Writes a new key to a synced temp file of mode 0600 and links it to the name
// The link fails when the name exists so a reader never sees a key cut short
func (f file) create() error {
	secret := make([]byte, size)
	// Read never fails since Go 1.24
	_, _ = rand.Read(secret)
	tmp, err := os.CreateTemp(filepath.Dir(string(f)), FileName+".*")
	if err != nil {
		return fmt.Errorf("%w: %w", ErrCreate, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	_, err = tmp.Write(secret)
	if err = errors.Join(err, tmp.Sync(), tmp.Close()); err == nil {
		err = os.Link(tmp.Name(), string(f))
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrCreate, err)
	}
	return nil
}
