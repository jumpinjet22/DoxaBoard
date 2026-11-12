package content

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

const sealedPrefix = "enc:v1:"

// Sealer encrypts connector secrets (API keys) before they're stored, so a
// copied database or backup doesn't leak them on its own.
type Sealer struct{ aead cipher.AEAD }

// NewSealer uses a 32-byte key.
func NewSealer(key []byte) (*Sealer, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("secret key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead: aead}, nil
}

// LoadOrCreateKey reads a hex key from path, creating it (0600) if missing.
// Keep this file out of database backups: that separation is the point.
func LoadOrCreateKey(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, []byte(hex.EncodeToString(key)+"\n"), 0o600); err != nil {
			return nil, err
		}
		return key, nil
	}
	if err != nil {
		return nil, err
	}
	key, err := hex.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("%s: expected 64 hex characters", path)
	}
	return key, nil
}

// Seal encrypts plaintext. Empty stays empty.
func (s *Sealer) Seal(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := s.aead.Seal(nonce, nonce, []byte(plain), nil)
	return sealedPrefix + base64.StdEncoding.EncodeToString(ct), nil
}

// Open decrypts a value produced by Seal.
func (s *Sealer) Open(sealed string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	b64, ok := strings.CutPrefix(sealed, sealedPrefix)
	if !ok {
		return "", errors.New("secret is not sealed")
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) < s.aead.NonceSize() {
		return "", errors.New("sealed secret is corrupt")
	}
	n := s.aead.NonceSize()
	plain, err := s.aead.Open(nil, raw[:n], raw[n:], nil)
	if err != nil {
		return "", errors.New("can't decrypt secret: wrong key or corrupt data")
	}
	return string(plain), nil
}
