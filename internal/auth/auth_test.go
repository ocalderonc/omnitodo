package auth

import (
	"strings"
	"testing"
	"time"
)

func TestPassphrase(t *testing.T) {
	h, err := HashPassphrase("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateHash(h); err != nil {
		t.Fatal(err)
	}
	if !VerifyPassphrase(h, "correct horse battery") {
		t.Error("right passphrase rejected")
	}
	if VerifyPassphrase(h, "correct horse batterY") {
		t.Error("wrong passphrase accepted")
	}
	if h2, _ := HashPassphrase("correct horse battery"); h2 == h {
		t.Error("salt is not random")
	}
	if _, err := HashPassphrase("short"); err == nil {
		t.Error("short passphrase accepted")
	}
	for _, bad := range []string{"", "plain", "bcrypt:1:2:3", strings.ReplaceAll(h, ":", "$"), strings.Replace(h, "600000", "10", 1)} {
		if ValidateHash(bad) == nil || VerifyPassphrase(bad, "x") {
			t.Errorf("malformed hash %q accepted", bad)
		}
	}
}

func TestSessions(t *testing.T) {
	if _, err := NewSessions("too short", time.Hour); err == nil {
		t.Error("short secret accepted")
	}
	s, _ := NewSessions(strings.Repeat("s", 32), time.Hour)
	other, _ := NewSessions(strings.Repeat("o", 32), time.Hour)
	now := time.Unix(1_800_000_000, 0)
	tok := s.Issue(now)

	if !s.Valid(tok, now.Add(59*time.Minute)) {
		t.Error("fresh session rejected")
	}
	if s.Valid(tok, now.Add(time.Hour)) {
		t.Error("expired session accepted")
	}
	if other.Valid(tok, now) {
		t.Error("session from another secret accepted")
	}
	exp, sig, _ := strings.Cut(tok, ".")
	forged := "9" + exp + "." + sig
	for _, bad := range []string{"", "x", exp, forged, exp + "." + sig + "x"} {
		if s.Valid(bad, now) {
			t.Errorf("token %q accepted", bad)
		}
	}
}

func TestLimiter(t *testing.T) {
	l := NewLimiter(2, time.Minute)
	now := time.Unix(0, 0)
	l.Fail(now)
	if !l.Allow(now) {
		t.Fatal("blocked after one failure")
	}
	l.Fail(now.Add(10 * time.Second))
	if l.Allow(now.Add(20 * time.Second)) {
		t.Error("allowed after max failures")
	}
	if !l.Allow(now.Add(61 * time.Second)) {
		t.Error("still blocked after the first failure left the window")
	}
}
