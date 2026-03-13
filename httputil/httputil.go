package httputil

import (
	"context"
	"encoding/json"
	"net/http"
)

type contextKey string

const userIDKey contextKey = "userId"

type errorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func Err(w http.ResponseWriter, status int, code, message string) {
	JSON(w, status, errorResponse{Error: code, Message: message})
}

func ErrNotFound(w http.ResponseWriter, what string) {
	Err(w, http.StatusNotFound, "NOT_FOUND", what+" not found")
}

func ErrBadRequest(w http.ResponseWriter, msg string) {
	Err(w, http.StatusBadRequest, "INVALID_REQUEST", msg)
}

func ErrInternal(w http.ResponseWriter, err error) {
	Err(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
}

func SetUserID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, userIDKey, id)
}

func UserIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(userIDKey).(string)
	return v
}
