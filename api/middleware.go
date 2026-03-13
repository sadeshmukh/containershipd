package api

import (
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"github.com/sadeshmukh/containershipd/httputil"
)

// AdminAuth validates the shared admin Bearer token.
func AdminAuth(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(auth, "Bearer ") {
				httputil.Err(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing bearer token")
				return
			}
			if strings.TrimPrefix(auth, "Bearer ") != secret {
				httputil.Err(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid token")
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
