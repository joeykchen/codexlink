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
	ok := manager.Verify(strings.ToLower(code), "10.0.0.1")
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

func TestPairingStatusTracksTerminalOutcome(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	manager := NewPairingManager("ws", PairingOptions{TTL: time.Minute, MaxAttempts: 1, Now: func() time.Time { return now }})
	if status := manager.Status(); status.State != PairingStateIdle {
		t.Fatalf("initial status = %+v", status)
	}
	code, _, err := manager.Create()
	if err != nil {
		t.Fatal(err)
	}
	if status := manager.Status(); status.State != PairingStateActive || !status.ChangedAt.Equal(now) {
		t.Fatalf("active status = %+v", status)
	}
	if result := manager.Verify(code, "127.0.0.1"); !result.OK {
		t.Fatalf("verification = %+v", result)
	}
	if status := manager.Status(); status.State != PairingStateConsumed || !status.ChangedAt.Equal(now) {
		t.Fatalf("consumed status = %+v", status)
	}
	_, _, _ = manager.Create()
	if result := manager.Verify("AAAA-AAAA", "127.0.0.2"); result.Failure != PairingTooManyAttempts {
		t.Fatalf("locked result = %+v", result)
	}
	if status := manager.Status(); status.State != PairingStateLocked {
		t.Fatalf("locked status = %+v", status)
	}
	_, _, _ = manager.Create()
	now = now.Add(2 * time.Minute)
	if status := manager.Status(); status.State != PairingStateExpired {
		t.Fatalf("expired status = %+v", status)
	}
	manager.Invalidate()
	if status := manager.Status(); status.State != PairingStateInvalidated {
		t.Fatalf("invalidated status = %+v", status)
	}
}

func TestPairingRateLimiterBoundsTrackedAddresses(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	manager := NewPairingManager("ws", PairingOptions{
		TTL: 2 * time.Minute, MaxAttempts: 3, IPLimit: 10, IPWindow: time.Minute,
		MaxTrackedIPs: 2, Now: func() time.Time { return now },
	})
	code, _, err := manager.Create()
	if err != nil {
		t.Fatal(err)
	}
	_ = manager.Verify("AAAA-AAAA", "192.0.2.1")
	_ = manager.Verify("BBBB-BBBB", "192.0.2.2")
	if result := manager.Verify(code, "192.0.2.3"); result.Failure != PairingRateLimited {
		t.Fatalf("third tracked address = %+v", result)
	}
	now = now.Add(time.Minute)
	if result := manager.Verify(code, "192.0.2.3"); !result.OK {
		t.Fatalf("expired address entries were not pruned: %+v", result)
	}
}

func TestPairingCodeFormatIsStrict(t *testing.T) {
	manager := NewPairingManager("ws", PairingOptions{})
	code, _, err := manager.Create()
	if err != nil {
		t.Fatal(err)
	}
	raw := strings.ReplaceAll(code, "-", "")
	if result := manager.Verify("  "+strings.ToLower(raw)+"  ", ""); !result.OK {
		t.Fatalf("plain normalized code = %+v", result)
	}

	malformed := []string{
		code[:4] + " " + code[5:],
		code[:4] + ":" + code[5:],
		code[:4] + "--" + code[5:],
		"prefix-" + code,
	}
	for _, value := range malformed {
		if normalizePairingCode(value) != "" {
			t.Fatalf("malformed code normalized successfully: %q", value)
		}
	}
}
