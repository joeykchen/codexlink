package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"strings"
	"sync"
	"time"
)

const pairingAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

type PairingFailure string

const (
	PairingInvalid         PairingFailure = "invalid"
	PairingExpired         PairingFailure = "expired"
	PairingTooManyAttempts PairingFailure = "too_many_attempts"
	PairingRateLimited     PairingFailure = "rate_limited"
	PairingNoSession       PairingFailure = "no_active_session"
)

type PairingResult struct {
	OK           bool
	SessionID    string
	Failure      PairingFailure
	AttemptsLeft int
}

type PairingState string

const (
	PairingStateIdle        PairingState = "idle"
	PairingStateActive      PairingState = "active"
	PairingStateConsumed    PairingState = "consumed"
	PairingStateExpired     PairingState = "expired"
	PairingStateLocked      PairingState = "locked"
	PairingStateInvalidated PairingState = "invalidated"
)

type PairingStatus struct {
	State     PairingState
	ChangedAt time.Time
}

type PairingOptions struct {
	TTL           time.Duration
	MaxAttempts   int
	IPLimit       int
	IPWindow      time.Duration
	MaxTrackedIPs int
	Now           func() time.Time
}

type pairingSession struct {
	ID           string
	Hash         [32]byte
	CreatedAt    time.Time
	ExpiresAt    time.Time
	AttemptsLeft int
}

type ipCounter struct {
	Count   int
	ResetAt time.Time
}

type PairingManager struct {
	mu            sync.Mutex
	ttl           time.Duration
	maxAttempts   int
	ipLimit       int
	ipWindow      time.Duration
	maxTrackedIPs int
	now           func() time.Time
	session       *pairingSession
	state         PairingState
	changedAt     time.Time
	ipHits        map[string]ipCounter
}

func NewPairingManager(_ string, options PairingOptions) *PairingManager {
	if options.TTL <= 0 {
		options.TTL = 5 * time.Minute
	}
	if options.MaxAttempts <= 0 {
		options.MaxAttempts = 5
	}
	if options.IPLimit <= 0 {
		options.IPLimit = 10
	}
	if options.IPWindow <= 0 {
		options.IPWindow = time.Minute
	}
	if options.MaxTrackedIPs <= 0 {
		options.MaxTrackedIPs = 2048
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &PairingManager{
		ttl:           options.TTL,
		maxAttempts:   options.MaxAttempts,
		ipLimit:       options.IPLimit,
		ipWindow:      options.IPWindow,
		maxTrackedIPs: options.MaxTrackedIPs,
		now:           options.Now,
		ipHits:        make(map[string]ipCounter),
	}
}

func randomString(alphabet string, length int) (string, error) {
	if length <= 0 || len(alphabet) == 0 || len(alphabet) > 256 {
		return "", fmt.Errorf("invalid random string parameters")
	}
	threshold := byte(256 - (256 % len(alphabet)))
	result := make([]byte, 0, length)
	buffer := make([]byte, length*2)
	for len(result) < length {
		if _, err := rand.Read(buffer); err != nil {
			return "", err
		}
		for _, value := range buffer {
			if value >= threshold {
				continue
			}
			result = append(result, alphabet[int(value)%len(alphabet)])
			if len(result) == length {
				break
			}
		}
	}
	return string(result), nil
}

func normalizePairingCode(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	switch len(value) {
	case 8:
	case 9:
		if value[4] != '-' {
			return ""
		}
		value = value[:4] + value[5:]
	default:
		return ""
	}
	for _, r := range value {
		if !strings.ContainsRune(pairingAlphabet, r) {
			return ""
		}
	}
	return value
}

func (m *PairingManager) Create() (code string, expiresAt time.Time, err error) {
	raw, err := randomString(pairingAlphabet, 8)
	if err != nil {
		return "", time.Time{}, err
	}
	sessionID, err := randomString("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789", 32)
	if err != nil {
		return "", time.Time{}, err
	}
	now := m.now()
	hash := sha256.Sum256([]byte(raw))
	m.mu.Lock()
	m.session = &pairingSession{ID: sessionID, Hash: hash, CreatedAt: now, ExpiresAt: now.Add(m.ttl), AttemptsLeft: m.maxAttempts}
	m.state = PairingStateActive
	m.changedAt = now
	m.mu.Unlock()
	return raw[:4] + "-" + raw[4:], now.Add(m.ttl), nil
}

func (m *PairingManager) allowIPLocked(ip string, now time.Time) bool {
	if ip == "" {
		return true
	}
	for address, counter := range m.ipHits {
		if !now.Before(counter.ResetAt) {
			delete(m.ipHits, address)
		}
	}
	counter, ok := m.ipHits[ip]
	if !ok {
		if len(m.ipHits) >= m.maxTrackedIPs {
			return false
		}
		m.ipHits[ip] = ipCounter{Count: 1, ResetAt: now.Add(m.ipWindow)}
		return true
	}
	counter.Count++
	m.ipHits[ip] = counter
	return counter.Count <= m.ipLimit
}

func (m *PairingManager) Verify(input, ip string) PairingResult {
	now := m.now()
	normalized := normalizePairingCode(input)
	inputHash := sha256.Sum256([]byte(normalized))
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.allowIPLocked(ip, now) {
		return PairingResult{Failure: PairingRateLimited}
	}
	if m.session == nil {
		return PairingResult{Failure: PairingNoSession}
	}
	if !now.Before(m.session.ExpiresAt) {
		m.session = nil
		m.state = PairingStateExpired
		m.changedAt = now
		return PairingResult{Failure: PairingExpired}
	}
	if m.session.AttemptsLeft <= 0 {
		m.session = nil
		m.state = PairingStateLocked
		m.changedAt = now
		return PairingResult{Failure: PairingTooManyAttempts}
	}
	match := subtle.ConstantTimeCompare(inputHash[:], m.session.Hash[:]) == 1
	if match {
		id := m.session.ID
		m.session = nil
		m.state = PairingStateConsumed
		m.changedAt = now
		return PairingResult{OK: true, SessionID: id}
	}
	m.session.AttemptsLeft--
	if m.session.AttemptsLeft <= 0 {
		m.session = nil
		m.state = PairingStateLocked
		m.changedAt = now
		return PairingResult{Failure: PairingTooManyAttempts}
	}
	return PairingResult{Failure: PairingInvalid, AttemptsLeft: m.session.AttemptsLeft}
}

func (m *PairingManager) Active() bool { return m.Status().State == PairingStateActive }

func (m *PairingManager) Status() PairingStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if m.session != nil && !now.Before(m.session.ExpiresAt) {
		m.session = nil
		m.state = PairingStateExpired
		m.changedAt = now
	}
	state := m.state
	if state == "" {
		state = PairingStateIdle
	}
	return PairingStatus{State: state, ChangedAt: m.changedAt}
}

func (m *PairingManager) Invalidate() {
	m.mu.Lock()
	m.session = nil
	m.state = PairingStateInvalidated
	m.changedAt = m.now()
	m.mu.Unlock()
}
