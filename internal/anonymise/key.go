package anonymise

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
)

// MinKeyLength is the shortest anonymisation key accepted.
const MinKeyLength = 16

// Key is the anonymisation secret. The same key gives the same fakes on every
// run; without it the mapping can be neither reproduced nor reversed.
type Key struct {
	secret []byte
	// ID identifies the key in provenance and the audit log without revealing it.
	ID string
}

// NewKey validates secret and derives its ID.
func NewKey(secret []byte) (Key, error) {
	s := []byte(strings.TrimSpace(string(secret)))
	if len(s) < MinKeyLength {
		return Key{}, fmt.Errorf("the anonymisation key must be at least %d characters", MinKeyLength)
	}
	id := sha256.Sum256(append([]byte("exemplar-anon-key-id\x00"), s...))
	return Key{secret: s, ID: "k-" + hex.EncodeToString(id[:6])}, nil
}

// RandomKey returns a new key that exists only in memory: its fakes match
// nothing made with any other key, so they cannot be used to test whether a
// real name is in a library anonymised under the server's key.
func RandomKey() (Key, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return Key{}, err
	}
	return NewKey([]byte(hex.EncodeToString(b[:])))
}

// Valid reports whether the key was made by NewKey.
func (k Key) Valid() bool {
	return len(k.secret) >= MinKeyLength
}

func (k Key) digest(kind, value string) []byte {
	m := hmac.New(sha256.New, k.secret)
	m.Write([]byte(kind))
	m.Write([]byte{0})
	m.Write([]byte(strings.ToLower(value)))
	return m.Sum(nil)
}

// Hash is the stored form of a real identifier: hex HMAC, never the value.
func (k Key) Hash(kind, value string) string {
	return hex.EncodeToString(k.digest(kind, value))
}

// Hex returns n hex characters derived from value, for short IDs.
func (k Key) Hex(kind, value string, n int) string {
	return hex.EncodeToString(k.digest(kind, value))[:n]
}

// Seed returns a generator seed derived from value.
func (k Key) Seed(kind, value string) uint64 {
	return binary.BigEndian.Uint64(k.digest(kind, value)[:8]) | 1
}
