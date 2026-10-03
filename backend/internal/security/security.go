package security

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

func Token() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func Digest(value string) string {
	d := sha256.Sum256([]byte(value))
	return hex.EncodeToString(d[:])
}

type Passwords struct{ gate chan struct{} }

func NewPasswords() *Passwords { return &Passwords{gate: make(chan struct{}, 1)} }

func (p *Passwords) derive(ctx context.Context, password string, salt []byte) ([]byte, error) {
	select {
	case p.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-p.gate }()
	return argon2.IDKey([]byte(password), salt, 3, 32*1024, 1, 32), nil
}

func (p *Passwords) Hash(ctx context.Context, password string) (string, error) {
	if len(password) < 12 || len(password) > 256 {
		return "", fmt.Errorf("password must contain 12 to 256 bytes")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := p.derive(ctx, password, salt)
	if err != nil {
		return "", err
	}
	return "$argon2id$v=19$m=32768,t=3,p=1$" + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(key), nil
}

func (p *Passwords) Verify(ctx context.Context, password, encoded string) bool {
	if len(password) > 256 {
		return false
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" || parts[3] != "m=32768,t=3,p=1" {
		return false
	}
	salt, e1 := base64.RawStdEncoding.DecodeString(parts[4])
	expected, e2 := base64.RawStdEncoding.DecodeString(parts[5])
	if e1 != nil || e2 != nil || len(salt) != 16 || len(expected) != 32 {
		return false
	}
	key, err := p.derive(ctx, password, salt)
	return err == nil && subtle.ConstantTimeCompare(key, expected) == 1
}
