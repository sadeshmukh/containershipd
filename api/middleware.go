package api

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"github.com/sadeshmukh/containershipd/httputil"
)

// AdminAuth validates the shared admin secret.
// Accepts the key via X-Admin-Key header (preferred — proxies don't touch it)
// or Authorization: Bearer <key> (fallback).
func AdminAuth(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := r.Header.Get("X-Admin-Key")
			if token == "" {
				auth := r.Header.Get("Authorization")
				token = strings.TrimPrefix(auth, "Bearer ")
			}
			if token == "" {
				httputil.Err(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing admin key")
				return
			}
			if subtle.ConstantTimeCompare([]byte(token), []byte(secret)) != 1 {
				httputil.Err(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid admin key")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// UserTokenAuth validates a short-lived user-scoped JWT via ?token= query param.
func UserTokenAuth(jwtSecret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tokenStr := r.URL.Query().Get("token")
			if tokenStr == "" {
				http.Error(w, "missing token", http.StatusUnauthorized)
				return
			}

			claims := &jwt.RegisteredClaims{}
			_, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
				if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, jwt.ErrSignatureInvalid
				}
				return []byte(jwtSecret), nil
			})
			if err != nil {
				http.Error(w, "invalid token", http.StatusUnauthorized)
				return
			}

			ctx := httputil.SetUserID(r.Context(), claims.Subject)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
