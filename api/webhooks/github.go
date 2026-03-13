package webhooks

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/sadeshmukh/containershipd/compose"
	"github.com/sadeshmukh/containershipd/config"
	"github.com/sadeshmukh/containershipd/models"
	"github.com/sadeshmukh/containershipd/store"
)

type GithubHandler struct {
	cfg         *config.Config
	deployments *store.Deployments
	composer    *compose.Manager
}

func NewGithubHandler(cfg *config.Config, deployments *store.Deployments, composer *compose.Manager) *GithubHandler {
	return &GithubHandler{cfg: cfg, deployments: deployments, composer: composer}
}

type pushEvent struct {
	Ref string `json:"ref"` // e.g. "refs/heads/main"
}

func (h *GithubHandler) Handle(w http.ResponseWriter, r *http.Request) {
	deploymentID := chi.URLParam(r, "deploymentId")

	d, err := h.deployments.Get(deploymentID)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if !d.Github.AutoRedeploy {
		w.WriteHeader(http.StatusOK)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 5*1024*1024))
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	// Validate HMAC signature.
	if d.WebhookSecret != "" {
		sig := r.Header.Get("X-Hub-Signature-256")
		if !validateSignature(body, d.WebhookSecret, sig) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
	}

	var event pushEvent
	if err := json.Unmarshal(body, &event); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	// Only redeploy on matching branch.
	expectedRef := fmt.Sprintf("refs/heads/%s", d.Github.Branch)
	if event.Ref != expectedRef {
		w.WriteHeader(http.StatusOK)
		return
	}

	if d.Status != models.StatusRunning && d.Status != models.StatusStopped {
		w.WriteHeader(http.StatusOK)
		return
	}

	slog.Info("github webhook: triggering redeploy", "deployment", deploymentID, "ref", event.Ref)
	w.WriteHeader(http.StatusAccepted)

	go func() {
		ctx := context.Background()

		h.deployments.UpdateStatus(d.ID, models.StatusProvisioning, "")

		token, err := h.deployments.GetGithubToken(d.ID)
		if err != nil || token == "" {
			slog.Error("webhook redeploy: failed to get token", "deployment", d.ID)
			h.deployments.UpdateStatus(d.ID, models.StatusError, "failed to retrieve github token")
			return
		}

		sha, err := h.composer.Redeploy(ctx, d, token)
		if err != nil {
			slog.Error("webhook redeploy failed", "deployment", d.ID, "error", err)
			h.deployments.UpdateStatus(d.ID, models.StatusError, err.Error())
			return
		}

		h.deployments.SetDeployed(d.ID, sha)
		h.deployments.UpdateStatus(d.ID, models.StatusRunning, "")
		slog.Info("webhook redeploy complete", "deployment", d.ID, "sha", sha)
	}()
}

func validateSignature(body []byte, secret, signature string) bool {
	const prefix = "sha256="
	if len(signature) < len(prefix) {
		return false
	}
	gotHex := signature[len(prefix):]
	gotBytes, err := hex.DecodeString(gotHex)
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := mac.Sum(nil)

	return hmac.Equal(expected, gotBytes)
}
