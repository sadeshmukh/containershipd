package admin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sadeshmukh/containershipd/compose"
	"github.com/sadeshmukh/containershipd/config"
	"github.com/sadeshmukh/containershipd/ghclient"
	"github.com/sadeshmukh/containershipd/httputil"
	"github.com/sadeshmukh/containershipd/models"
	"github.com/sadeshmukh/containershipd/store"
)

type DeploymentHandler struct {
	cfg         *config.Config
	users       *store.Users
	deployments *store.Deployments
	metrics     *store.Metrics
	composer    *compose.Manager
	ghClient    *ghclient.Client
}

func NewDeploymentHandler(
	cfg *config.Config,
	users *store.Users,
	deployments *store.Deployments,
	metrics *store.Metrics,
	composer *compose.Manager,
	ghClient *ghclient.Client,
) *DeploymentHandler {
	return &DeploymentHandler{
		cfg:         cfg,
		users:       users,
		deployments: deployments,
		metrics:     metrics,
		composer:    composer,
		ghClient:    ghClient,
	}
}

func (h *DeploymentHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID string `json:"userId"`
		Name   string `json:"name"`
		Github struct {
			RepoURL      string `json:"repoUrl"`
			Branch       string `json:"branch"`
			ComposeFile  string `json:"composeFile"`
			GithubToken  string `json:"githubToken"`
			AutoRedeploy bool   `json:"autoRedeploy"`
		} `json:"github"`
		ResourceLimits models.ResourceLimits `json:"resourceLimits"`
		Env            map[string]string     `json:"env"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.ErrBadRequest(w, "invalid JSON")
		return
	}
	if req.UserID == "" || req.Name == "" || req.Github.RepoURL == "" || req.Github.GithubToken == "" {
		httputil.ErrBadRequest(w, "userId, name, github.repoUrl, and github.githubToken are required")
		return
	}

	if req.Github.Branch == "" {
		req.Github.Branch = "main"
	}
	if req.Github.ComposeFile == "" {
		req.Github.ComposeFile = "docker-compose.yml"
	}
	if req.ResourceLimits.CPULimit == 0 {
		req.ResourceLimits.CPULimit = 0.5
	}
	if req.ResourceLimits.MemoryLimitMb == 0 {
		req.ResourceLimits.MemoryLimitMb = 512
	}
	if req.ResourceLimits.StorageLimitGb == 0 {
		req.ResourceLimits.StorageLimitGb = 2.0
	}
	if req.Env == nil {
		req.Env = map[string]string{}
	}

	user, err := h.users.Get(req.UserID)
	if errors.Is(err, store.ErrNotFound) {
		httputil.ErrNotFound(w, "user")
		return
	}
	if err != nil {
		httputil.ErrInternal(w, err)
		return
	}
	if user.Status == models.UserStatusSuspended {
		httputil.Err(w, http.StatusForbidden, "USER_SUSPENDED", "user account is suspended")
		return
	}

	usage, err := h.users.GetUsage(user.ID)
	if err != nil {
		httputil.ErrInternal(w, err)
		return
	}
	if err := checkQuota(user.Quota, *usage, req.ResourceLimits); err != nil {
		httputil.Err(w, http.StatusTooManyRequests, "QUOTA_EXCEEDED", err.Error())
		return
	}

	webhookSecret, err := randomHex(16)
	if err != nil {
		httputil.ErrInternal(w, err)
		return
	}

	d, err := h.deployments.Create(store.CreateDeploymentParams{
		UserID:         req.UserID,
		Name:           req.Name,
		RepoURL:        req.Github.RepoURL,
		Branch:         req.Github.Branch,
		ComposeFile:    req.Github.ComposeFile,
		GithubToken:    req.Github.GithubToken,
		AutoRedeploy:   req.Github.AutoRedeploy,
		ResourceLimits: req.ResourceLimits,
		Env:            req.Env,
		WebhookSecret:  webhookSecret,
	})
	if err != nil {
		httputil.ErrInternal(w, err)
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		h.provision(ctx, d, req.Github.GithubToken)
	}()

	httputil.JSON(w, http.StatusAccepted, d)
}

func (h *DeploymentHandler) provision(ctx context.Context, d *models.Deployment, githubToken string) {
	sha, err := h.composer.Deploy(ctx, d, githubToken)
	if err != nil {
		slog.Error("deployment failed", "id", d.ID, "error", err)
		h.deployments.UpdateStatus(d.ID, models.StatusError, err.Error())
		return
	}

	h.deployments.SetDeployed(d.ID, sha)
	h.deployments.UpdateStatus(d.ID, models.StatusRunning, "")

	updated, _ := h.deployments.Get(d.ID)
	if updated != nil {
		if ports, err := h.composer.GetPortMappings(ctx, updated); err == nil && len(ports) > 0 {
			h.deployments.SetPorts(d.ID, ports)
		}
	}

	if d.Github.AutoRedeploy && h.cfg.WebhookBaseURL != "" {
		webhookURL := h.cfg.WebhookBaseURL + "/webhooks/github/" + d.ID
		webhookID, err := h.ghClient.RegisterWebhook(ctx, d.Github.RepoURL, githubToken, webhookURL, d.WebhookSecret)
		if err != nil {
			slog.Warn("webhook registration failed", "deployment", d.ID, "error", err)
		} else {
			h.deployments.SetWebhookID(d.ID, webhookID)
		}
	}

	slog.Info("deployment ready", "id", d.ID, "sha", sha)
}

func (h *DeploymentHandler) List(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	deployments, err := h.deployments.List(store.ListDeploymentsParams{
		UserID: r.URL.Query().Get("userId"),
		Status: r.URL.Query().Get("status"),
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		httputil.ErrInternal(w, err)
		return
	}
	if deployments == nil {
		deployments = []*models.Deployment{}
	}
	httputil.JSON(w, http.StatusOK, map[string]any{"deployments": deployments})
}

func (h *DeploymentHandler) Get(w http.ResponseWriter, r *http.Request) {
	d, ok := h.resolveDeployment(w, r)
	if !ok {
		return
	}
	httputil.JSON(w, http.StatusOK, d)
}

func (h *DeploymentHandler) Update(w http.ResponseWriter, r *http.Request) {
	d, ok := h.resolveDeployment(w, r)
	if !ok {
		return
	}

	var req struct {
		Name   *string `json:"name"`
		Github *struct {
			Branch       *string `json:"branch"`
			ComposeFile  *string `json:"composeFile"`
			AutoRedeploy *bool   `json:"autoRedeploy"`
			GithubToken  *string `json:"githubToken"`
		} `json:"github"`
		ResourceLimits *models.ResourceLimits `json:"resourceLimits"`
		Env            map[string]string      `json:"env"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.ErrBadRequest(w, "invalid JSON")
		return
	}

	params := store.UpdateDeploymentParams{Name: req.Name, Env: req.Env}
	if req.Github != nil {
		params.Branch = req.Github.Branch
		params.ComposeFile = req.Github.ComposeFile
		params.AutoRedeploy = req.Github.AutoRedeploy
		params.GithubToken = req.Github.GithubToken
	}
	if req.ResourceLimits != nil {
		params.ResourceLimits = req.ResourceLimits
	}

	updated, err := h.deployments.Update(d.ID, params)
	if err != nil {
		httputil.ErrInternal(w, err)
		return
	}

	if d.Status == models.StatusRunning && (req.ResourceLimits != nil || req.Env != nil) {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			if err := h.composer.Reconfigure(ctx, updated); err != nil {
				slog.Warn("reconfigure failed", "deployment", d.ID, "error", err)
				h.deployments.UpdateStatus(d.ID, models.StatusError, err.Error())
			}
		}()
	}

	httputil.JSON(w, http.StatusOK, updated)
}

func (h *DeploymentHandler) Delete(w http.ResponseWriter, r *http.Request) {
	d, ok := h.resolveDeployment(w, r)
	if !ok {
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if d.Github.WebhookID != 0 {
			if token, err := h.deployments.GetGithubToken(d.ID); err == nil && token != "" {
				h.ghClient.DeleteWebhook(ctx, d.Github.RepoURL, token, d.Github.WebhookID)
			}
		}
		h.composer.Teardown(ctx, d)
	}()

	if err := h.deployments.Delete(d.ID); err != nil {
		httputil.ErrInternal(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *DeploymentHandler) Start(w http.ResponseWriter, r *http.Request) {
	d, ok := h.resolveDeployment(w, r)
	if !ok {
		return
	}
	if d.Status != models.StatusStopped {
		httputil.Err(w, http.StatusConflict, "CONFLICT", "deployment is not stopped")
		return
	}
	h.deployments.UpdateStatus(d.ID, models.StatusProvisioning, "")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := h.composer.Start(ctx, d); err != nil {
			h.deployments.UpdateStatus(d.ID, models.StatusError, err.Error())
			return
		}
		h.deployments.UpdateStatus(d.ID, models.StatusRunning, "")
	}()
	d.Status = models.StatusProvisioning
	httputil.JSON(w, http.StatusAccepted, d)
}

func (h *DeploymentHandler) Stop(w http.ResponseWriter, r *http.Request) {
	d, ok := h.resolveDeployment(w, r)
	if !ok {
		return
	}
	if d.Status != models.StatusRunning {
		httputil.Err(w, http.StatusConflict, "CONFLICT", "deployment is not running")
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := h.composer.Stop(ctx, d); err != nil {
			slog.Warn("stop failed", "deployment", d.ID, "error", err)
			return
		}
		h.deployments.UpdateStatus(d.ID, models.StatusStopped, "")
	}()
	httputil.JSON(w, http.StatusAccepted, map[string]string{"status": "stopping"})
}

func (h *DeploymentHandler) Restart(w http.ResponseWriter, r *http.Request) {
	d, ok := h.resolveDeployment(w, r)
	if !ok {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := h.composer.Restart(ctx, d); err != nil {
			slog.Warn("restart failed", "deployment", d.ID, "error", err)
		}
	}()
	httputil.JSON(w, http.StatusAccepted, map[string]string{"status": "restarting"})
}

func (h *DeploymentHandler) Redeploy(w http.ResponseWriter, r *http.Request) {
	d, ok := h.resolveDeployment(w, r)
	if !ok {
		return
	}
	h.deployments.UpdateStatus(d.ID, models.StatusProvisioning, "")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		token, err := h.deployments.GetGithubToken(d.ID)
		if err != nil {
			h.deployments.UpdateStatus(d.ID, models.StatusError, "failed to retrieve github token")
			return
		}
		sha, err := h.composer.Redeploy(ctx, d, token)
		if err != nil {
			h.deployments.UpdateStatus(d.ID, models.StatusError, err.Error())
			return
		}
		h.deployments.SetDeployed(d.ID, sha)
		h.deployments.UpdateStatus(d.ID, models.StatusRunning, "")
	}()
	httputil.JSON(w, http.StatusAccepted, map[string]string{"status": "redeploying"})
}

func (h *DeploymentHandler) Metrics(w http.ResponseWriter, r *http.Request) {
	d, ok := h.resolveDeployment(w, r)
	if !ok {
		return
	}
	m, err := h.metrics.Latest(d.ID)
	if err != nil {
		httputil.ErrInternal(w, err)
		return
	}
	httputil.JSON(w, http.StatusOK, m)
}

func (h *DeploymentHandler) MetricsHistory(w http.ResponseWriter, r *http.Request) {
	d, ok := h.resolveDeployment(w, r)
	if !ok {
		return
	}

	from := time.Now().Add(-1 * time.Hour)
	to := time.Now()
	if v := r.URL.Query().Get("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			from = t
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			to = t
		}
	}

	history, err := h.metrics.History(d.ID, from, to)
	if err != nil {
		httputil.ErrInternal(w, err)
		return
	}
	httputil.JSON(w, http.StatusOK, map[string]any{"history": history})
}

func (h *DeploymentHandler) Logs(w http.ResponseWriter, r *http.Request) {
	d, ok := h.resolveDeployment(w, r)
	if !ok {
		return
	}

	rc, err := h.composer.Logs(r.Context(), d, r.URL.Query().Get("service"), false)
	if err != nil {
		httputil.ErrInternal(w, err)
		return
	}
	defer rc.Close()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)

	buf := make([]byte, 4096)
	for {
		n, err := rc.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		if err != nil {
			break
		}
	}
}

func (h *DeploymentHandler) resolveDeployment(w http.ResponseWriter, r *http.Request) (*models.Deployment, bool) {
	id := chi.URLParam(r, "id")
	d, err := h.deployments.Get(id)
	if errors.Is(err, store.ErrNotFound) {
		httputil.ErrNotFound(w, "deployment")
		return nil, false
	}
	if err != nil {
		httputil.ErrInternal(w, err)
		return nil, false
	}
	return d, true
}

func checkQuota(q models.Quota, usage models.Usage, requested models.ResourceLimits) error {
	if usage.Deployments >= q.MaxDeployments {
		return errors.New("deployment limit reached")
	}
	if usage.CPUCores+requested.CPULimit > q.MaxCPUCores {
		return errors.New("CPU quota would be exceeded")
	}
	if float64(usage.MemoryMb+requested.MemoryLimitMb) > float64(q.MaxMemoryMb) {
		return errors.New("memory quota would be exceeded")
	}
	if usage.StorageGb+requested.StorageLimitGb > q.MaxStorageGb {
		return errors.New("storage quota would be exceeded")
	}
	return nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
