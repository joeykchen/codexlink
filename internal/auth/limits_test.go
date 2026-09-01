package auth

import (
	"testing"
	"time"
)

func TestRequestLimiterBoundsRateAndTrackedAddresses(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	limiter := newRequestLimiter(2, 3, time.Minute, 2)
	if !limiter.Allow("one", now) || !limiter.Allow("one", now) {
		t.Fatal("allowed requests were rejected")
	}
	if limiter.Allow("one", now) {
		t.Fatal("per-address limit was not enforced")
	}
	if !limiter.Allow("two", now) {
		t.Fatal("second address should use remaining global capacity")
	}
	if limiter.Allow("three", now) {
		t.Fatal("global or address tracking limit was not enforced")
	}
	if !limiter.Allow("three", now.Add(time.Minute)) {
		t.Fatal("expired rate window was not pruned")
	}
}
