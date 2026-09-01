package auth

import (
	"sync"
	"time"
)

const defaultLimitWindow = time.Minute

type OAuthLimits struct {
	MaxPending          int
	RegistrationPerIP   int
	RegistrationGlobal  int
	AuthorizationPerIP  int
	AuthorizationGlobal int
	MaxTrackedAddresses int
	Window              time.Duration
}

func defaultOAuthLimits() OAuthLimits {
	return OAuthLimits{
		MaxPending:          128,
		RegistrationPerIP:   10,
		RegistrationGlobal:  100,
		AuthorizationPerIP:  30,
		AuthorizationGlobal: 300,
		MaxTrackedAddresses: 2048,
		Window:              defaultLimitWindow,
	}
}

func normalizeOAuthLimits(value OAuthLimits) OAuthLimits {
	defaults := defaultOAuthLimits()
	if value.MaxPending <= 0 {
		value.MaxPending = defaults.MaxPending
	}
	if value.RegistrationPerIP <= 0 {
		value.RegistrationPerIP = defaults.RegistrationPerIP
	}
	if value.RegistrationGlobal <= 0 {
		value.RegistrationGlobal = defaults.RegistrationGlobal
	}
	if value.AuthorizationPerIP <= 0 {
		value.AuthorizationPerIP = defaults.AuthorizationPerIP
	}
	if value.AuthorizationGlobal <= 0 {
		value.AuthorizationGlobal = defaults.AuthorizationGlobal
	}
	if value.MaxTrackedAddresses <= 0 {
		value.MaxTrackedAddresses = defaults.MaxTrackedAddresses
	}
	if value.Window <= 0 {
		value.Window = defaults.Window
	}
	return value
}

type rateBucket struct {
	windowStart time.Time
	count       int
}

// requestLimiter evaluates per-address and global limits under one lock so a
// rejected request never consumes capacity from the other counter.
type requestLimiter struct {
	mu           sync.Mutex
	perAddress   int
	global       int
	window       time.Duration
	maxAddresses int
	globalBucket rateBucket
	addresses    map[string]rateBucket
}

func newRequestLimiter(perAddress, global int, window time.Duration, maxAddresses int) *requestLimiter {
	return &requestLimiter{
		perAddress:   perAddress,
		global:       global,
		window:       window,
		maxAddresses: maxAddresses,
		addresses:    make(map[string]rateBucket),
	}
}

func (l *requestLimiter) Allow(address string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	for key, bucket := range l.addresses {
		if !now.Before(bucket.windowStart.Add(l.window)) {
			delete(l.addresses, key)
		}
	}
	if l.globalBucket.windowStart.IsZero() || !now.Before(l.globalBucket.windowStart.Add(l.window)) {
		l.globalBucket = rateBucket{windowStart: now}
	}

	bucket, tracked := l.addresses[address]
	if !tracked {
		if len(l.addresses) >= l.maxAddresses {
			return false
		}
		bucket = rateBucket{windowStart: now}
	} else if !now.Before(bucket.windowStart.Add(l.window)) {
		bucket = rateBucket{windowStart: now}
	}

	if bucket.count >= l.perAddress || l.globalBucket.count >= l.global {
		return false
	}
	bucket.count++
	l.globalBucket.count++
	l.addresses[address] = bucket
	return true
}
