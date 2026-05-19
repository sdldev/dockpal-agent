package auth

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
)

// TokenMiddleware validates the agent token from the Authorization header
// or the query parameter (for WebSocket connections).
func TokenMiddleware(token string) func(http.Handler) http.Handler {
	tokenBytes := []byte(token)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Check Authorization header first
			authHeader := r.Header.Get("Authorization")
			if authHeader != "" {
				parts := strings.SplitN(authHeader, " ", 2)
				if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") &&
					subtle.ConstantTimeCompare([]byte(parts[1]), tokenBytes) == 1 {
					next.ServeHTTP(w, r)
					return
				}
				jsonError(w, http.StatusUnauthorized, "invalid token")
				return
			}

			// For WebSocket, check query param
			if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("token")), tokenBytes) == 1 {
				next.ServeHTTP(w, r)
				return
			}

			jsonError(w, http.StatusUnauthorized, "missing authorization")
		})
	}
}

func jsonError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
