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

type PairingOptions struct {
	TTL         time.Duration
	MaxAttempts int
	IPLimit     int
	IPWindow    time.Duration
	Now         func() time.Time
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
	mu          sync.Mutex
	workspaceID string
	ttl         time.Duration
	maxAttempts int
	ipLimit     int
	ipWindow    time.Duration
	now         func() time.Time
	session     *pairingSession
	ipHits      map[string]ipCounter
}

func NewPairingManager(workspaceID string, options PairingOptions) *PairingManager {
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
	if options.Now == nil {
		options.Now = time.Now
	}
	return &PairingManager{
		workspaceID: workspaceID,
		ttl:         options.TTL,
		maxAttempts: options.MaxAttempts,
		ipLimit:     options.IPLimit,
		ipWindow:    options.IPWindow,
		now:         options.Now,
		ipHits:      make(map[string]ipCounter),
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
	value = strings.ToUpper(value)
	var builder strings.Builder
	for _, r := range value {
		if strings.ContainsRune(pairingAlphabet, r) {
			builder.WriteRune(r)
		}
	}
	return builder.String()
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
	m.mu.Unlock()
	return raw[:4] + "-" + raw[4:], now.Add(m.ttl), nil
}

func (m *PairingManager) allowIPLocked(ip string, now time.Time) bool {
	if ip == "" {
		return true
	}
	counter, ok := m.ipHits[ip]
	if !ok || !now.Before(counter.ResetAt) {
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
	if now.After(m.session.ExpiresAt) {
		m.session = nil
		return PairingResult{Failure: PairingExpired}
	}
	if m.session.AttemptsLeft <= 0 {
		m.session = nil
		return PairingResult{Failure: PairingTooManyAttempts}
	}
	match := subtle.ConstantTimeCompare(inputHash[:], m.session.Hash[:]) == 1
	if match {
		id := m.session.ID
		m.session = nil
		return PairingResult{OK: true, SessionID: id}
	}
	m.session.AttemptsLeft--
	if m.session.AttemptsLeft <= 0 {
		m.session = nil
		return PairingResult{Failure: PairingTooManyAttempts}
	}
	return PairingResult{Failure: PairingInvalid, AttemptsLeft: m.session.AttemptsLeft}
}

func (m *PairingManager) Active() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.session == nil {
		return false
	}
	if m.now().After(m.session.ExpiresAt) {
		m.session = nil
		return false
	}
	return true
}

func (m *PairingManager) Invalidate() {
	m.mu.Lock()
	m.session = nil
	m.mu.Unlock()
}
