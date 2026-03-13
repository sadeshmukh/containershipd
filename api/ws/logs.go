package ws

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"github.com/sadeshmukh/containershipd/compose"
	"github.com/sadeshmukh/containershipd/config"
	"github.com/sadeshmukh/containershipd/httputil"
	"github.com/sadeshmukh/containershipd/store"
)

var upgrader = websocket.Upgrader{
	CheckOrigin:     func(r *http.Request) bool { return true },
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
}

type LogHandler struct {
	cfg         *config.Config
	deployments *store.Deployments
	composer    *compose.Manager
}

func NewLogHandler(cfg *config.Config, deployments *store.Deployments, composer *compose.Manager) *LogHandler {
	return &LogHandler{cfg: cfg, deployments: deployments, composer: composer}
}

func (h *LogHandler) Logs(w http.ResponseWriter, r *http.Request) {
	deploymentID := chi.URLParam(r, "id")
	userID := httputil.UserIDFromContext(r.Context())

	d, err := h.deployments.Get(deploymentID)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if d.UserID != userID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Warn("ws upgrade failed", "error", err)
		return
	}
	defer conn.Close()

	logRC, err := h.composer.Logs(r.Context(), d, r.URL.Query().Get("service"), true)
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("error: "+err.Error()))
		return
	}
	defer logRC.Close()

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			case <-r.Context().Done():
				return
			}
		}
	}()

	buf := make([]byte, 4096)
	for {
		n, err := logRC.Read(buf)
		if n > 0 {
			if werr := conn.WriteMessage(websocket.TextMessage, buf[:n]); werr != nil {
				break
			}
		}
		if err != nil {
			break
		}
	}
}

func (h *LogHandler) Events(w http.ResponseWriter, r *http.Request) {
	deploymentID := chi.URLParam(r, "id")
	userID := httputil.UserIDFromContext(r.Context())

	d, err := h.deployments.Get(deploymentID)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if d.UserID != userID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	lastStatus := d.Status
	for {
		select {
		case <-ticker.C:
			current, err := h.deployments.Get(deploymentID)
			if err != nil {
				return
			}
			if current.Status != lastStatus {
				conn.WriteJSON(map[string]string{
					"type":   "status_change",
					"status": string(current.Status),
				})
				lastStatus = current.Status
			}
		case <-r.Context().Done():
			return
		}
	}
}
