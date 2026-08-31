package auth

import (
	"strings"
	"testing"
	"time"
)

func TestPairingOneTimeAttemptsExpiryAndRateLimit(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	manager := NewPairingManager("ws", PairingOptions{
		TTL: time.Minute, MaxAttempts: 2, IPLimit: 2, IPWindow: time.Minute,
		Now: func() time.Time { return now },
	})
	code, expires, err := manager.Create()
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != 9 || code[4] != '-' || !expires.Equal(now.Add(time.Minute)) {
		t.Fatalf("unexpected code or expiry: %q %v", code, expires)
	}
	wrong := manager.Verify("AAAA-AAAA", "10.0.0.1")
	if wrong.Failure != PairingInvalid || wrong.AttemptsLeft != 1 {
		t.Fatalf("unexpected wrong-code result: %#v", wrong)
	}
	ok := manager.Verify(strings.ToLower(strings.ReplaceAll(code, "-", " ")), "10.0.0.1")
	if !ok.OK || ok.SessionID == "" {
		t.Fatalf("verification failed: %#v", ok)
	}
	if again := manager.Verify(code, "10.0.0.2"); again.Failure != PairingNoSession {
		t.Fatalf("code should be one-time: %#v", again)
	}

	code, _, _ = manager.Create()
	now = now.Add(2 * time.Minute)
	if expired := manager.Verify(code, "10.0.0.3"); expired.Failure != PairingExpired {
		t.Fatalf("expected expiry, got %#v", expired)
	}

	now = now.Add(time.Minute)
	code, _, _ = manager.Create()
	_ = manager.Verify("BBBB-BBBB", "10.0.0.4")
	_ = manager.Verify("CCCC-CCCC", "10.0.0.4")
	if limited := manager.Verify(code, "10.0.0.4"); limited.Failure != PairingRateLimited {
		t.Fatalf("expected rate limit, got %#v", limited)
	}
}
