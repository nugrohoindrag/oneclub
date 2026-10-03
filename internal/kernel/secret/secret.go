// Package secret encrypts values at rest (integration credentials, TOTP
// secrets) with AES-256-GCM using a key derived from APP_SECRET.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
)

// Box encrypts and decrypts small payloads.
type Box struct {
	aead cipher.AEAD
	mac  []byte
}

// NewBox derives an AES-256 key from appSecret.
func NewBox(appSecret string) (*Box, error) {
	if len(appSecret) < 32 {
		return nil, errors.New("secret: APP_SECRET must be at least 32 characters")
	}
	k := sha256.Sum256([]byte("oneclub:enc:" + appSecret))
	block, err := aes.NewCipher(k[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	m := sha256.Sum256([]byte("oneclub:mac:" + appSecret))
	return &Box{aead: aead, mac: m[:]}, nil
}

// Seal encrypts plaintext; output is nonce||ciphertext.
func (b *Box) Seal(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Open decrypts a value produced by Seal.
func (b *Box) Open(sealed []byte) ([]byte, error) {
	ns := b.aead.NonceSize()
	if len(sealed) < ns {
		return nil, errors.New("secret: ciphertext too short")
	}
	return b.aead.Open(nil, sealed[:ns], sealed[ns:], nil)
}

// RandomToken returns a URL-safe random token with n bytes of entropy.
func RandomToken(n int) string {
	buf := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

// HashToken returns the hex SHA-256 of a bearer token; only hashes are stored.
func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// Equal compares two strings in constant time.
func Equal(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
