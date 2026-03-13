package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"

	"github.com/sadeshmukh/containershipd/config"
	"github.com/sadeshmukh/containershipd/httputil"
	"github.com/sadeshmukh/containershipd/models"
	"github.com/sadeshmukh/containershipd/store"
)

type UserHandler struct {
	cfg         *config.Config
	users       *store.Users
	deployments *store.Deployments
}

func NewUserHandler(cfg *config.Config, users *store.Users, deployments *store.Deployments) *UserHandler {
	return &UserHandler{cfg: cfg, users: users, deployments: deployments}
}

func (h *UserHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ExternalID string       `json:"externalId"`
		Quota      models.Quota `json:"quota"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.ErrBadRequest(w, "invalid JSON")
		return
	}
	if req.ExternalID == "" {
		httputil.ErrBadRequest(w, "externalId is required")
		return
	}

	q := req.Quota
	if q.MaxDeployments == 0 {
		q.MaxDeployments = 3
	}
	if q.MaxCPUCores == 0 {
		q.MaxCPUCores = 2.0
	}
	if q.MaxMemoryMb == 0 {
		q.MaxMemoryMb = 2048
	}
	if q.MaxStorageGb == 0 {
		q.MaxStorageGb = 10.0
	}
	if q.MaxBandwidthGbMonth == 0 {
		q.MaxBandwidthGbMonth = 100.0
	}

	user, err := h.users.Create(store.CreateUserParams{
		ExternalID: req.ExternalID,
		Quota:      q,
	})
	if err != nil {
		httputil.ErrInternal(w, err)
		return
	}
	httputil.JSON(w, http.StatusCreated, user)
}

func (h *UserHandler) List(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	users, err := h.users.List(store.ListUsersParams{
		Status: r.URL.Query().Get("status"),
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		httputil.ErrInternal(w, err)
		return
	}
	if users == nil {
		users = []*models.User{}
	}
	httputil.JSON(w, http.StatusOK, map[string]any{"users": users})
}

func (h *UserHandler) Get(w http.ResponseWriter, r *http.Request) {
	user, ok := h.resolveUser(w, r)
	if !ok {
		return
	}
	httputil.JSON(w, http.StatusOK, user)
}

func (h *UserHandler) Update(w http.ResponseWriter, r *http.Request) {
	user, ok := h.resolveUser(w, r)
	if !ok {
		return
	}

	var req struct {
		ExternalID *string       `json:"externalId"`
		Status     *string       `json:"status"`
		Quota      *models.Quota `json:"quota"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.ErrBadRequest(w, "invalid JSON")
		return
	}

	if req.Status != nil && *req.Status != string(models.UserStatusActive) && *req.Status != string(models.UserStatusSuspended) {
		httputil.ErrBadRequest(w, "status must be 'active' or 'suspended'")
		return
	}

	updated, err := h.users.Update(user.ID, store.UpdateUserParams{
		ExternalID: req.ExternalID,
		Status:     req.Status,
		Quota:      req.Quota,
	})
	if err != nil {
		httputil.ErrInternal(w, err)
		return
	}
	httputil.JSON(w, http.StatusOK, updated)
}

func (h *UserHandler) Delete(w http.ResponseWriter, r *http.Request) {
	user, ok := h.resolveUser(w, r)
	if !ok {
		return
	}

	if r.URL.Query().Get("force") != "true" {
		deployments, err := h.deployments.List(store.ListDeploymentsParams{UserID: user.ID, Limit: 1})
		if err != nil {
			httputil.ErrInternal(w, err)
			return
		}
		if len(deployments) > 0 {
			httputil.Err(w, http.StatusConflict, "CONFLICT", "user has active deployments; delete them first or use ?force=true")
			return
		}
	}

	if err := h.users.Delete(user.ID); err != nil {
		httputil.ErrInternal(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *UserHandler) IssueToken(w http.ResponseWriter, r *http.Request) {
	user, ok := h.resolveUser(w, r)
	if !ok {
		return
	}

	exp := time.Now().Add(time.Hour)
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject:   user.ID,
		ExpiresAt: jwt.NewNumericDate(exp),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
	})
	signed, err := token.SignedString([]byte(h.cfg.JWTSecret))
	if err != nil {
		httputil.ErrInternal(w, err)
		return
	}
	httputil.JSON(w, http.StatusOK, map[string]any{
		"token":     signed,
		"expiresAt": exp.Format(time.RFC3339),
	})
}

func (h *UserHandler) Usage(w http.ResponseWriter, r *http.Request) {
	user, ok := h.resolveUser(w, r)
	if !ok {
		return
	}

	usage, err := h.users.GetUsage(user.ID)
	if err != nil {
		httputil.ErrInternal(w, err)
		return
	}

	available := models.Quota{
		MaxDeployments:      user.Quota.MaxDeployments - usage.Deployments,
		MaxCPUCores:         user.Quota.MaxCPUCores - usage.CPUCores,
		MaxMemoryMb:         user.Quota.MaxMemoryMb - usage.MemoryMb,
		MaxStorageGb:        user.Quota.MaxStorageGb - usage.StorageGb,
		MaxBandwidthGbMonth: user.Quota.MaxBandwidthGbMonth - usage.BandwidthGbThisMonth,
	}

	httputil.JSON(w, http.StatusOK, map[string]any{
		"userId":    user.ID,
		"quota":     user.Quota,
		"usage":     usage,
		"available": available,
	})
}

func (h *UserHandler) resolveUser(w http.ResponseWriter, r *http.Request) (*models.User, bool) {
	id := chi.URLParam(r, "id")
	user, err := h.users.Get(id)
	if errors.Is(err, store.ErrNotFound) {
		httputil.ErrNotFound(w, "user")
		return nil, false
	}
	if err != nil {
		httputil.ErrInternal(w, err)
		return nil, false
	}
	return user, true
}
