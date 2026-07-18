// Package auth owns admin authentication: reading its own configuration,
// issuing and verifying signed access tokens, and gating admin routes.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"carbon_go/internal/env"
	"carbon_go/internal/render"
)

const (
	// DefaultSession is the token lifetime when ADMIN_SESSION_TTL is unset.
	DefaultSession = 12 * time.Hour
	// MaxSession caps how long a configured session may last.
	MaxSession = 7 * 24 * time.Hour
	// TokenPrefix identifies tokens minted by this service.
	TokenPrefix = "cgadm1"
)

// Config holds the admin credentials and signing material read from the
// environment.
type Config struct {
	StaticToken   string
	Username      string
	Password      string
	SigningSecret string
	SessionTTL    time.Duration
}

// AccessTokenClaims is the payload carried inside an admin access token.
type AccessTokenClaims struct {
	Sub      string `json:"sub"`
	Username string `json:"username"`
	Iat      int64  `json:"iat"`
	Exp      int64  `json:"exp"`
	Jti      string `json:"jti"`
}

// LoadConfig reads admin auth settings and rejects unusable combinations.
func LoadConfig() (Config, error) {
	cfg := Config{
		StaticToken:   strings.TrimSpace(os.Getenv("ADMIN_TOKEN")),
		Username:      strings.TrimSpace(os.Getenv("ADMIN_USERNAME")),
		Password:      strings.TrimSpace(os.Getenv("ADMIN_PASSWORD")),
		SigningSecret: strings.TrimSpace(env.FirstNonEmpty(os.Getenv("ADMIN_JWT_SECRET"), os.Getenv("JWT_SECRET"))),
		SessionTTL:    resolveSessionTTL(),
	}

	hasStaticToken := cfg.StaticToken != ""
	hasCredentials := cfg.Username != "" && cfg.Password != ""
	if !hasStaticToken && !hasCredentials {
		return Config{}, errors.New("admin auth is not configured (set ADMIN_TOKEN or ADMIN_USERNAME and ADMIN_PASSWORD)")
	}
	if hasCredentials && cfg.SigningSecret == "" && !hasStaticToken {
		return Config{}, errors.New("JWT_SECRET or ADMIN_JWT_SECRET is required when ADMIN_USERNAME and ADMIN_PASSWORD are set")
	}

	return cfg, nil
}

func resolveSessionTTL() time.Duration {
	raw := strings.TrimSpace(os.Getenv("ADMIN_SESSION_TTL"))
	if raw == "" {
		return DefaultSession
	}

	ttl, err := time.ParseDuration(raw)
	if err != nil || ttl <= 0 {
		return DefaultSession
	}
	if ttl > MaxSession {
		return MaxSession
	}
	return ttl
}

// RequireToken gates a request on a valid admin token, writing the error
// response itself and reporting whether the caller may proceed.
func RequireToken(w http.ResponseWriter, r *http.Request) bool {
	cfg, err := LoadConfig()
	if err != nil {
		render.JSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": err.Error(),
		})
		return false
	}

	provided := ExtractToken(r)
	if provided == "" {
		render.JSON(w, http.StatusUnauthorized, map[string]any{
			"status":  "error",
			"message": "missing admin token",
		})
		return false
	}

	if cfg.StaticToken != "" && subtle.ConstantTimeCompare([]byte(provided), []byte(cfg.StaticToken)) == 1 {
		return true
	}

	if cfg.SigningSecret != "" {
		if _, err := VerifyAccessToken(provided, cfg.SigningSecret, time.Now()); err == nil {
			return true
		}
	}

	render.JSON(w, http.StatusUnauthorized, map[string]any{
		"status":  "error",
		"message": "unauthorized",
	})
	return false
}

// ExtractToken pulls the admin token from either supported header.
func ExtractToken(r *http.Request) string {
	provided := strings.TrimSpace(r.Header.Get("X-Admin-Token"))
	if provided != "" {
		return provided
	}

	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(auth) >= 7 && strings.EqualFold(auth[:7], "Bearer ") {
		return strings.TrimSpace(auth[7:])
	}

	return ""
}

// IssueAccessToken mints a signed admin token and reports when it expires.
func IssueAccessToken(username, signingSecret string, ttl time.Duration, now time.Time) (string, time.Time, error) {
	if strings.TrimSpace(signingSecret) == "" {
		return "", time.Time{}, errors.New("signing secret is required")
	}
	if ttl <= 0 {
		ttl = DefaultSession
	}

	jtiRaw := make([]byte, 16)
	if _, err := rand.Read(jtiRaw); err != nil {
		return "", time.Time{}, fmt.Errorf("generate token id: %w", err)
	}

	expiresAt := now.Add(ttl).UTC()
	claims := AccessTokenClaims{
		Sub:      "admin",
		Username: strings.TrimSpace(username),
		Iat:      now.UTC().Unix(),
		Exp:      expiresAt.Unix(),
		Jti:      base64.RawURLEncoding.EncodeToString(jtiRaw),
	}

	rawClaims, err := json.Marshal(claims)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("marshal token claims: %w", err)
	}

	payload := base64.RawURLEncoding.EncodeToString(rawClaims)
	signature := signTokenPayload(payload, signingSecret)
	token := TokenPrefix + "." + payload + "." + base64.RawURLEncoding.EncodeToString(signature)

	return token, expiresAt, nil
}

// VerifyAccessToken checks a token's signature, subject, and expiry.
func VerifyAccessToken(token, signingSecret string, now time.Time) (AccessTokenClaims, error) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 || parts[0] != TokenPrefix {
		return AccessTokenClaims{}, errors.New("invalid token format")
	}

	payload := parts[1]
	signatureRaw, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return AccessTokenClaims{}, errors.New("invalid token signature")
	}

	expectedSignature := signTokenPayload(payload, signingSecret)
	if subtle.ConstantTimeCompare(signatureRaw, expectedSignature) != 1 {
		return AccessTokenClaims{}, errors.New("invalid token signature")
	}

	claimsRaw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return AccessTokenClaims{}, errors.New("invalid token payload")
	}

	var claims AccessTokenClaims
	if err := json.Unmarshal(claimsRaw, &claims); err != nil {
		return AccessTokenClaims{}, errors.New("invalid token claims")
	}

	if claims.Sub != "admin" {
		return AccessTokenClaims{}, errors.New("invalid token subject")
	}
	if claims.Exp <= now.UTC().Unix() {
		return AccessTokenClaims{}, errors.New("token expired")
	}

	return claims, nil
}

func signTokenPayload(payload, signingSecret string) []byte {
	mac := hmac.New(sha256.New, []byte(signingSecret))
	_, _ = mac.Write([]byte(payload))
	return mac.Sum(nil)
}
