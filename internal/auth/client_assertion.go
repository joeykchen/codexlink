package auth

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	chatGPTClientID = "https://chatgpt.com/oauth/client.json"
	chatGPTJWKSURI  = "https://chatgpt.com/oauth/jwks.json"
)

type ClientAssertionVerifier interface {
	Verify(context.Context, string, Client, string, time.Time) (string, time.Time, error)
}

func clientAssertionIssuer(assertion string) string {
	if len(assertion) == 0 || len(assertion) > 16*1024 {
		return ""
	}
	parts := strings.Split(assertion, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(payload) > 8192 {
		return ""
	}
	var claims struct {
		Issuer string `json:"iss"`
	}
	if json.Unmarshal(payload, &claims) != nil || !isMetadataClientID(claims.Issuer) {
		return ""
	}
	return claims.Issuer
}

type ChatGPTAssertionVerifier struct {
	Client   *http.Client
	CacheTTL time.Duration

	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	expiresAt time.Time
}

func NewChatGPTAssertionVerifier() *ChatGPTAssertionVerifier {
	return &ChatGPTAssertionVerifier{
		Client: &http.Client{
			Timeout:       8 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		CacheTTL: 5 * time.Minute,
	}
}

func (v *ChatGPTAssertionVerifier) Verify(ctx context.Context, assertion string, client Client, audience string, now time.Time) (string, time.Time, error) {
	if client.ID != chatGPTClientID || client.JWKSURI != chatGPTJWKSURI || client.TokenEndpointSigningAlg != "RS256" {
		return "", time.Time{}, fmt.Errorf("unsupported private_key_jwt client")
	}
	if len(assertion) == 0 || len(assertion) > 16*1024 {
		return "", time.Time{}, fmt.Errorf("client assertion has invalid size")
	}
	parts := strings.Split(assertion, ".")
	if len(parts) != 3 {
		return "", time.Time{}, fmt.Errorf("client assertion must be a compact JWT")
	}
	decode := base64.RawURLEncoding.DecodeString
	headerJSON, err := decode(parts[0])
	if err != nil || len(headerJSON) > 4096 {
		return "", time.Time{}, fmt.Errorf("invalid client assertion header")
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil || header.Alg != "RS256" || header.Kid == "" || len(header.Kid) > 256 {
		return "", time.Time{}, fmt.Errorf("client assertion requires an RS256 key id")
	}
	claimsJSON, err := decode(parts[1])
	if err != nil || len(claimsJSON) > 8192 {
		return "", time.Time{}, fmt.Errorf("invalid client assertion claims")
	}
	var claims struct {
		Issuer    string          `json:"iss"`
		Subject   string          `json:"sub"`
		Audience  json.RawMessage `json:"aud"`
		Expires   json.Number     `json:"exp"`
		IssuedAt  json.Number     `json:"iat"`
		NotBefore json.Number     `json:"nbf"`
		JWTID     string          `json:"jti"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(claimsJSON)))
	decoder.UseNumber()
	if err := decoder.Decode(&claims); err != nil {
		return "", time.Time{}, fmt.Errorf("invalid client assertion claims")
	}
	if claims.Issuer != client.ID || claims.Subject != client.ID || !jwtAudienceContains(claims.Audience, audience) {
		return "", time.Time{}, fmt.Errorf("client assertion identity or audience mismatch")
	}
	expiresUnix, err := claims.Expires.Int64()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("client assertion expiration is required")
	}
	expiresAt := time.Unix(expiresUnix, 0)
	if !now.Before(expiresAt) || expiresAt.After(now.Add(5*time.Minute)) {
		return "", time.Time{}, fmt.Errorf("client assertion expiration is outside the allowed window")
	}
	if claims.IssuedAt != "" {
		issuedUnix, err := claims.IssuedAt.Int64()
		if err != nil || time.Unix(issuedUnix, 0).After(now.Add(time.Minute)) || time.Unix(issuedUnix, 0).Before(now.Add(-5*time.Minute)) {
			return "", time.Time{}, fmt.Errorf("client assertion issued-at time is invalid")
		}
	}
	if claims.NotBefore != "" {
		notBeforeUnix, err := claims.NotBefore.Int64()
		if err != nil || time.Unix(notBeforeUnix, 0).After(now.Add(time.Minute)) {
			return "", time.Time{}, fmt.Errorf("client assertion is not active")
		}
	}
	if claims.JWTID == "" || len(claims.JWTID) > 256 {
		return "", time.Time{}, fmt.Errorf("client assertion jti is required")
	}
	key, err := v.key(ctx, header.Kid, now)
	if err != nil {
		return "", time.Time{}, err
	}
	signature, err := decode(parts[2])
	if err != nil {
		return "", time.Time{}, fmt.Errorf("invalid client assertion signature")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature); err != nil {
		return "", time.Time{}, fmt.Errorf("invalid client assertion signature")
	}
	return claims.JWTID, expiresAt, nil
}

func jwtAudienceContains(raw json.RawMessage, expected string) bool {
	var single string
	if json.Unmarshal(raw, &single) == nil {
		return single == expected
	}
	var multiple []string
	if json.Unmarshal(raw, &multiple) != nil {
		return false
	}
	for _, audience := range multiple {
		if audience == expected {
			return true
		}
	}
	return false
}

func (v *ChatGPTAssertionVerifier) key(ctx context.Context, kid string, now time.Time) (*rsa.PublicKey, error) {
	v.mu.Lock()
	if now.Before(v.expiresAt) {
		key := v.keys[kid]
		v.mu.Unlock()
		if key == nil {
			return nil, fmt.Errorf("client assertion key id is unknown")
		}
		return key, nil
	}
	v.mu.Unlock()

	client := v.Client
	if client == nil {
		client = NewChatGPTAssertionVerifier().Client
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, chatGPTJWKSURI, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch ChatGPT signing keys: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ChatGPT signing keys returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 256*1024+1))
	if err != nil || len(body) > 256*1024 {
		return nil, fmt.Errorf("invalid ChatGPT signing keys response")
	}
	keys, err := parseRSAJWKS(body)
	if err != nil {
		return nil, err
	}
	ttl := v.CacheTTL
	if ttl <= 0 || ttl > time.Hour {
		ttl = 5 * time.Minute
	}
	v.mu.Lock()
	v.keys, v.expiresAt = keys, now.Add(ttl)
	key := keys[kid]
	v.mu.Unlock()
	if key == nil {
		return nil, fmt.Errorf("client assertion key id is unknown")
	}
	return key, nil
}

func parseRSAJWKS(body []byte) (map[string]*rsa.PublicKey, error) {
	var document struct {
		Keys []struct {
			KTY string `json:"kty"`
			Use string `json:"use"`
			Alg string `json:"alg"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(body, &document); err != nil || len(document.Keys) == 0 || len(document.Keys) > 32 {
		return nil, errors.New("invalid ChatGPT JWKS")
	}
	keys := make(map[string]*rsa.PublicKey)
	for _, item := range document.Keys {
		if item.KTY != "RSA" || item.Alg != "RS256" || (item.Use != "" && item.Use != "sig") || item.Kid == "" || len(item.Kid) > 256 {
			continue
		}
		modulus, errN := base64.RawURLEncoding.DecodeString(item.N)
		exponent, errE := base64.RawURLEncoding.DecodeString(item.E)
		if errN != nil || errE != nil || len(modulus) < 256 || len(modulus) > 512 || len(exponent) == 0 || len(exponent) > 4 {
			continue
		}
		e := 0
		for _, value := range exponent {
			e = e<<8 | int(value)
		}
		if e < 3 || e%2 == 0 {
			continue
		}
		key := &rsa.PublicKey{N: new(big.Int).SetBytes(modulus), E: e}
		if _, err := x509.MarshalPKIXPublicKey(key); err != nil {
			continue
		}
		keys[item.Kid] = key
	}
	if len(keys) == 0 {
		return nil, errors.New("ChatGPT JWKS contains no usable RS256 keys")
	}
	return keys, nil
}
