package handlers

// Admin login and session introspection.

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"carbon_go/internal/auth"
	"carbon_go/internal/render"
)

type adminAuthLoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (a *App) adminAuthLoginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		render.JSON(w, http.StatusMethodNotAllowed, map[string]any{
			"status":  "error",
			"message": "method not allowed",
		})
		return
	}

	cfg, err := auth.LoadConfig()
	if err != nil {
		render.JSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": err.Error(),
		})
		return
	}
	if cfg.Username == "" || cfg.Password == "" {
		render.JSON(w, http.StatusServiceUnavailable, map[string]any{
			"status":  "error",
			"message": "admin login is not configured (set ADMIN_USERNAME and ADMIN_PASSWORD)",
		})
		return
	}
	if cfg.SigningSecret == "" {
		render.JSON(w, http.StatusServiceUnavailable, map[string]any{
			"status":  "error",
			"message": "admin login requires JWT_SECRET or ADMIN_JWT_SECRET",
		})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	defer r.Body.Close()

	var payload adminAuthLoginRequest
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&payload); err != nil {
		render.JSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": "invalid JSON body",
		})
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		render.JSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": "expected a single JSON object",
		})
		return
	}

	username := strings.TrimSpace(payload.Username)
	password := strings.TrimSpace(payload.Password)

	usernameMatches := subtle.ConstantTimeCompare([]byte(username), []byte(cfg.Username)) == 1
	passwordMatches := subtle.ConstantTimeCompare([]byte(password), []byte(cfg.Password)) == 1
	if !usernameMatches || !passwordMatches {
		render.JSON(w, http.StatusUnauthorized, map[string]any{
			"status":  "error",
			"message": "invalid credentials",
		})
		return
	}

	token, expiresAt, err := auth.IssueAccessToken(username, cfg.SigningSecret, cfg.SessionTTL, time.Now())
	if err != nil {
		log.Printf("admin auth token issue failed: %v", err)
		render.JSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": "failed to issue access token",
		})
		return
	}

	render.JSON(w, http.StatusOK, map[string]any{
		"status": "success",
		"data": map[string]any{
			"token_type":   "Bearer",
			"access_token": token,
			"expires_at":   expiresAt.UTC().Format(time.RFC3339),
			"expires_in":   int(cfg.SessionTTL.Seconds()),
			"username":     username,
		},
	})
}

func (a *App) adminAuthMeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		render.JSON(w, http.StatusMethodNotAllowed, map[string]any{
			"status":  "error",
			"message": "method not allowed",
		})
		return
	}
	if !auth.RequireToken(w, r) {
		return
	}

	data := map[string]any{
		"authenticated": true,
		"auth_type":     "static_token",
	}

	cfg, err := auth.LoadConfig()
	if err == nil && cfg.SigningSecret != "" {
		provided := auth.ExtractToken(r)
		claims, verifyErr := auth.VerifyAccessToken(provided, cfg.SigningSecret, time.Now())
		if verifyErr == nil {
			data["auth_type"] = "bearer"
			data["username"] = claims.Username
			data["expires_at"] = time.Unix(claims.Exp, 0).UTC().Format(time.RFC3339)
		}
	}

	render.JSON(w, http.StatusOK, map[string]any{
		"status": "success",
		"data":   data,
	})
}
