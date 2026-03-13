package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/sadeshmukh/containershipd/api/admin"
	"github.com/sadeshmukh/containershipd/api/webhooks"
	"github.com/sadeshmukh/containershipd/api/ws"
	"github.com/sadeshmukh/containershipd/compose"
	"github.com/sadeshmukh/containershipd/config"
	"github.com/sadeshmukh/containershipd/ghclient"
	"github.com/sadeshmukh/containershipd/store"
)

func NewRouter(
	cfg *config.Config,
	users *store.Users,
	deployments *store.Deployments,
	metrics *store.Metrics,
	composer *compose.Manager,
	ghClient *ghclient.Client,
) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)

	userH := admin.NewUserHandler(cfg, users, deployments)
	deployH := admin.NewDeploymentHandler(cfg, users, deployments, metrics, composer, ghClient)
	webhookH := webhooks.NewGithubHandler(cfg, deployments, composer)
	wsH := ws.NewLogHandler(cfg, deployments, composer)

	r.Route("/admin", func(r chi.Router) {
		r.Use(AdminAuth(cfg.AdminSecret))

		r.Post("/users", userH.Create)
		r.Get("/users", userH.List)
		r.Get("/users/{id}", userH.Get)
		r.Patch("/users/{id}", userH.Update)
		r.Delete("/users/{id}", userH.Delete)
		r.Post("/users/{id}/token", userH.IssueToken)
		r.Get("/users/{id}/usage", userH.Usage)

		r.Post("/deployments", deployH.Create)
		r.Get("/deployments", deployH.List)
		r.Get("/deployments/{id}", deployH.Get)
		r.Patch("/deployments/{id}", deployH.Update)
		r.Delete("/deployments/{id}", deployH.Delete)
		r.Post("/deployments/{id}/start", deployH.Start)
		r.Post("/deployments/{id}/stop", deployH.Stop)
		r.Post("/deployments/{id}/restart", deployH.Restart)
		r.Post("/deployments/{id}/redeploy", deployH.Redeploy)
		r.Get("/deployments/{id}/metrics", deployH.Metrics)
		r.Get("/deployments/{id}/metrics/history", deployH.MetricsHistory)
		r.Get("/deployments/{id}/logs", deployH.Logs)
	})

	r.Post("/webhooks/github/{deploymentId}", webhookH.Handle)

	r.Route("/ws", func(r chi.Router) {
		r.Use(UserTokenAuth(cfg.JWTSecret))
		r.Get("/deployments/{id}/logs", wsH.Logs)
		r.Get("/deployments/{id}/events", wsH.Events)
	})

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	return r
}
