// Package auth is the single-user passphrase login: a PBKDF2 passphrase hash
// and an HMAC-signed session cookie. Standard library only.
package auth

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	iterations = 600_000 // OWASP 2023 recommendation for PBKDF2-HMAC-SHA256
	keyLen     = 32
	prefix     = "pbkdf2-sha256"

	MinSecretLen = 32
)

var b64 = base64.RawStdEncoding

// HashPassphrase returns "pbkdf2-sha256$<iterations>$<salt>$<key>".
func HashPassphrase(passphrase string) (string, error) {
	if len(passphrase) < 8 {
		return "", errors.New("passphrase must be at least 8 characters")
	}
	salt := make([]byte, 16)
	rand.Read(salt)
	key, err := pbkdf2.Key(sha256.New, passphrase, salt, iterations, keyLen)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s$%d$%s$%s", prefix, iterations, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// ValidateHash reports whether encoded is a hash HashPassphrase could produce,
// so a typo in the environment fails at startup instead of at the first login.
func ValidateHash(encoded string) error {
	_, _, _, err := parseHash(encoded)
	return err
}

func parseHash(encoded string) (iter int, salt, key []byte, err error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != prefix {
		return 0, nil, nil, errors.New("passphrase hash: want pbkdf2-sha256$<iter>$<salt>$<key> (run `omnitodo hash-passphrase`)")
	}
	if iter, err = strconv.Atoi(parts[1]); err != nil || iter < 100_000 {
		return 0, nil, nil, errors.New("passphrase hash: bad iteration count")
	}
	if salt, err = b64.DecodeString(parts[2]); err != nil || len(salt) < 16 {
		return 0, nil, nil, errors.New("passphrase hash: bad salt")
	}
	if key, err = b64.DecodeString(parts[3]); err != nil || len(key) != keyLen {
		return 0, nil, nil, errors.New("passphrase hash: bad key")
	}
	return iter, salt, key, nil
}

// VerifyPassphrase compares in constant time.
func VerifyPassphrase(encoded, passphrase string) bool {
	iter, salt, want, err := parseHash(encoded)
	if err != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, passphrase, salt, iter, keyLen)
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

// Sessions are "<unix expiry>.<base64 HMAC(expiry)>". There is one user, so
// the session carries nothing but its expiry; rotating the secret logs out
// every device.
type Sessions struct {
	secret []byte
	ttl    time.Duration
}

func NewSessions(secret string, ttl time.Duration) (*Sessions, error) {
	if len(secret) < MinSecretLen {
		return nil, fmt.Errorf("session secret must be at least %d characters (openssl rand -base64 48)", MinSecretLen)
	}
	return &Sessions{secret: []byte(secret), ttl: ttl}, nil
}

func (s *Sessions) TTL() time.Duration { return s.ttl }

func (s *Sessions) sign(payload string) string {
	m := hmac.New(sha256.New, s.secret)
	m.Write([]byte("omnitodo-session:" + payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (s *Sessions) Issue(now time.Time) string {
	exp := strconv.FormatInt(now.Add(s.ttl).Unix(), 10)
	return exp + "." + s.sign(exp)
}

func (s *Sessions) Valid(token string, now time.Time) bool {
	exp, sig, ok := strings.Cut(token, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(s.sign(exp))) {
		return false
	}
	unix, err := strconv.ParseInt(exp, 10, 64)
	return err == nil && now.Unix() < unix
}

// Limiter allows at most max failed logins per window, across all clients.
// Behind Coolify's proxy every request comes from the proxy's address, so a
// per-IP limit would be a global one anyway; this just says so honestly.
type Limiter struct {
	mu       sync.Mutex
	max      int
	window   time.Duration
	failures []time.Time
}

func NewLimiter(max int, window time.Duration) *Limiter {
	return &Limiter{max: max, window: window}
}

func (l *Limiter) prune(now time.Time) {
	keep := l.failures[:0]
	for _, t := range l.failures {
		if now.Sub(t) < l.window {
			keep = append(keep, t)
		}
	}
	l.failures = keep
}

// Allow reports whether a login attempt may be checked now.
func (l *Limiter) Allow(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(now)
	return len(l.failures) < l.max
}

func (l *Limiter) Fail(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failures = append(l.failures, now)
}
