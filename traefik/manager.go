package traefik

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
)

const networkName = "csd-traefik"

// Manager owns the Traefik container lifecycle. It writes a docker-compose
// file to $DATA_DIR/traefik/ and manages the container via the Docker CLI.
type Manager struct {
	dataDir   string
	acmeEmail string
}

func New(dataDir, acmeEmail string) *Manager {
	return &Manager{dataDir: dataDir, acmeEmail: acmeEmail}
}

// EnsureRunning writes the Traefik compose file and starts the container if
// it is not already running. Errors are non-fatal — the daemon can run
// without Traefik when no deployments need a subdomain.
func (m *Manager) EnsureRunning(ctx context.Context) error {
	dir := filepath.Join(m.dataDir, "traefik")
	letsencryptDir := filepath.Join(dir, "letsencrypt")
	if err := os.MkdirAll(letsencryptDir, 0700); err != nil {
		return fmt.Errorf("traefik: create dirs: %w", err)
	}

	// Pre-create acme.json with 0600 so Traefik does not complain.
	acmeFile := filepath.Join(letsencryptDir, "acme.json")
	if _, err := os.Stat(acmeFile); os.IsNotExist(err) {
		if err := os.WriteFile(acmeFile, []byte{}, 0600); err != nil {
			return fmt.Errorf("traefik: create acme.json: %w", err)
		}
	}

	composePath := filepath.Join(dir, "docker-compose.yml")
	if err := m.writeComposeFile(composePath, letsencryptDir); err != nil {
		return fmt.Errorf("traefik: write compose file: %w", err)
	}

	out, err := exec.CommandContext(ctx, "docker", "compose",
		"-p", "csd-traefik",
		"-f", composePath,
		"up", "-d",
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("traefik: compose up: %s: %w", out, err)
	}

	slog.Info("traefik started", "network", networkName)
	return nil
}

func (m *Manager) writeComposeFile(path, letsencryptDir string) error {
	content := fmt.Sprintf(`services:
  traefik:
    image: traefik:v3.2
    restart: unless-stopped
    command:
      - "--providers.docker=true"
      - "--providers.docker.exposedbydefault=false"
      - "--providers.docker.network=%s"
      - "--entrypoints.web.address=:80"
      - "--entrypoints.websecure.address=:443"
      - "--entrypoints.web.http.redirections.entrypoint.to=websecure"
      - "--entrypoints.web.http.redirections.entrypoint.scheme=https"
      - "--certificatesresolvers.letsencrypt.acme.httpchallenge=true"
      - "--certificatesresolvers.letsencrypt.acme.httpchallenge.entrypoint=web"
      - "--certificatesresolvers.letsencrypt.acme.email=%s"
      - "--certificatesresolvers.letsencrypt.acme.storage=/letsencrypt/acme.json"
    environment:
      - DOCKER_API_VERSION=1.41
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - %s:/letsencrypt
    networks:
      - csd-traefik

networks:
  csd-traefik:
    name: %s
`, networkName, m.acmeEmail, letsencryptDir, networkName)

	return os.WriteFile(path, []byte(content), 0600)
}
