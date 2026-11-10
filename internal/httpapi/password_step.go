package httpapi

import (
	"github.com/bryu234/pentaract-v2/internal/auth"
	"net/http"
	"strings"
)

// verifyPassword advances the UI only. A session still requires login with both factors.
func (s *Server) verifyPassword(w http.ResponseWriter, r *http.Request) {
	var in struct{ Email, Password string }
	if !decodeJSON(w, r, &in) {
		return
	}
	key := accountKey(in.Email)
	if blocked, wait := s.LoginGuard.accountBlocked(key); blocked {
		rateDenied(w, wait)
		return
	}
	var hash string
	var enabled bool
	err := s.DB.QueryRow(r.Context(), `SELECT password_hash,totp_enabled FROM users WHERE email=$1`, strings.ToLower(strings.TrimSpace(in.Email))).Scan(&hash, &enabled)
	if err != nil || !enabled || !auth.VerifyPassword(hash, in.Password) {
		s.LoginGuard.failure(key)
		problem(w, 401, "invalid credentials")
		return
	}
	writeJSON(w, 200, map[string]bool{"otp_required": true})
}
