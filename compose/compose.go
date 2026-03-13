package compose

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/sadeshmukh/containershipd/models"
)

// Manager manages Docker Compose deployments on disk.
type Manager struct {
	dataDir string
}

func NewManager(dataDir string) *Manager {
	return &Manager{dataDir: dataDir}
}

// ProjectName returns a stable, valid Docker Compose project name for a deployment ID.
func ProjectName(id string) string {
	clean := strings.ReplaceAll(id, "-", "")
	if len(clean) > 12 {
		clean = clean[:12]
	}
	return "csd" + clean
}

func (m *Manager) deployDir(id string) string {
	return filepath.Join(m.dataDir, "deployments", id)
}

func (m *Manager) repoDir(id string) string {
	return filepath.Join(m.deployDir(id), "repo")
}

func (m *Manager) overrideFile(id string) string {
	return filepath.Join(m.deployDir(id), "docker-compose.override.yml")
}

func (m *Manager) envFile(id string) string {
	return filepath.Join(m.deployDir(id), ".env")
}

// Deploy clones the repo, applies resource limits, and starts all services.
// Returns the deployed commit SHA.
func (m *Manager) Deploy(ctx context.Context, d *models.Deployment, githubToken string) (string, error) {
	if err := os.MkdirAll(m.deployDir(d.ID), 0755); err != nil {
		return "", err
	}

	repoDir := m.repoDir(d.ID)

	if _, err := os.Stat(repoDir); os.IsNotExist(err) {
		if err := cloneRepo(ctx, d.Github.RepoURL, githubToken, repoDir, d.Github.Branch); err != nil {
			return "", fmt.Errorf("clone failed: %w", err)
		}
	} else {
		if err := pullRepo(ctx, repoDir, d.Github.Branch, githubToken); err != nil {
			slog.Warn("git pull failed, using existing code", "deployment", d.ID, "error", err)
		}
	}

	sha, err := getHeadSHA(repoDir)
	if err != nil {
		return "", err
	}

	composePath := filepath.Join(repoDir, d.Github.ComposeFile)
	services, err := ServiceNames(composePath)
	if err != nil {
		return "", fmt.Errorf("invalid compose file: %w", err)
	}

	if err := writeEnvFile(m.envFile(d.ID), d.Env); err != nil {
		return "", err
	}

	if err := writeOverride(m.overrideFile(d.ID), services, d.ResourceLimits); err != nil {
		return "", err
	}

	if err := m.composeUp(ctx, d.ID, composePath); err != nil {
		return "", err
	}

	return sha, nil
}

// Redeploy pulls latest code and restarts.
func (m *Manager) Redeploy(ctx context.Context, d *models.Deployment, githubToken string) (string, error) {
	repoDir := m.repoDir(d.ID)
	if err := pullRepo(ctx, repoDir, d.Github.Branch, githubToken); err != nil {
		return "", fmt.Errorf("pull failed: %w", err)
	}

	sha, err := getHeadSHA(repoDir)
	if err != nil {
		return "", err
	}

	composePath := filepath.Join(repoDir, d.Github.ComposeFile)
	services, err := ServiceNames(composePath)
	if err != nil {
		return "", err
	}

	if err := writeEnvFile(m.envFile(d.ID), d.Env); err != nil {
		return "", err
	}

	if err := writeOverride(m.overrideFile(d.ID), services, d.ResourceLimits); err != nil {
		return "", err
	}

	if err := m.composeUp(ctx, d.ID, composePath); err != nil {
		return "", err
	}

	return sha, nil
}

// Reconfigure applies updated resource limits or env without pulling new code.
func (m *Manager) Reconfigure(ctx context.Context, d *models.Deployment) error {
	repoDir := m.repoDir(d.ID)
	composePath := filepath.Join(repoDir, d.Github.ComposeFile)

	services, err := ServiceNames(composePath)
	if err != nil {
		return err
	}

	if err := writeEnvFile(m.envFile(d.ID), d.Env); err != nil {
		return err
	}

	if err := writeOverride(m.overrideFile(d.ID), services, d.ResourceLimits); err != nil {
		return err
	}

	return m.composeUp(ctx, d.ID, composePath)
}

// Start starts a stopped deployment.
func (m *Manager) Start(ctx context.Context, d *models.Deployment) error {
	composePath := filepath.Join(m.repoDir(d.ID), d.Github.ComposeFile)
	return m.composeUp(ctx, d.ID, composePath)
}

// Stop stops all services without removing volumes.
func (m *Manager) Stop(ctx context.Context, d *models.Deployment) error {
	out, err := m.compose(ctx, d.ID, d.Github.ComposeFile, "stop")
	if err != nil {
		return fmt.Errorf("compose stop: %s: %w", out, err)
	}
	return nil
}

// Restart restarts all services.
func (m *Manager) Restart(ctx context.Context, d *models.Deployment) error {
	out, err := m.compose(ctx, d.ID, d.Github.ComposeFile, "restart")
	if err != nil {
		return fmt.Errorf("compose restart: %s: %w", out, err)
	}
	return nil
}

// Teardown stops and removes all containers, networks, and volumes.
func (m *Manager) Teardown(ctx context.Context, d *models.Deployment) error {
	out, err := m.compose(ctx, d.ID, d.Github.ComposeFile, "down", "--volumes", "--remove-orphans")
	if err != nil {
		// Log but don't fail — containers may already be gone.
		slog.Warn("compose down had errors", "deployment", d.ID, "output", string(out))
	}
	return os.RemoveAll(m.deployDir(d.ID))
}

// Logs returns a reader for streaming logs. Caller must close it.
func (m *Manager) Logs(ctx context.Context, d *models.Deployment, service string, follow bool) (io.ReadCloser, error) {
	args := []string{
		"compose",
		"-p", ProjectName(d.ID),
		"-f", filepath.Join(m.repoDir(d.ID), d.Github.ComposeFile),
		"-f", m.overrideFile(d.ID),
		"logs", "--timestamps",
	}
	if follow {
		args = append(args, "--follow")
	}
	args = append(args, "--tail=200")
	if service != "" {
		args = append(args, service)
	}

	cmd := exec.CommandContext(ctx, "docker", args...)
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	go func() {
		cmd.Wait()
		pw.Close()
	}()

	return pr, nil
}

// GetPortMappings inspects running containers to extract host→container port mappings.
func (m *Manager) GetPortMappings(ctx context.Context, d *models.Deployment) ([]models.Port, error) {
	out, err := exec.CommandContext(ctx, "docker", "compose",
		"-p", ProjectName(d.ID),
		"-f", filepath.Join(m.repoDir(d.ID), d.Github.ComposeFile),
		"-f", m.overrideFile(d.ID),
		"ps", "--format", "{{.Name}}\t{{.Ports}}",
	).Output()
	if err != nil {
		return nil, err
	}

	var ports []models.Port
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 || parts[1] == "" {
			continue
		}
		containerName := parts[0]
		service := serviceFromContainerName(containerName, ProjectName(d.ID))

		// Ports format: "0.0.0.0:12345->80/tcp, ..."
		for _, mapping := range strings.Split(parts[1], ", ") {
			mapping = strings.TrimSpace(mapping)
			var hostPort, containerPort int
			if n, _ := fmt.Sscanf(mapping, "0.0.0.0:%d->%d", &hostPort, &containerPort); n == 2 {
				ports = append(ports, models.Port{
					Service:       service,
					HostPort:      hostPort,
					ContainerPort: containerPort,
				})
			}
		}
	}
	return ports, nil
}

// ---- internal helpers ----

func (m *Manager) composeUp(ctx context.Context, id, composePath string) error {
	proj := ProjectName(id)
	cmd := exec.CommandContext(ctx, "docker", "compose",
		"-p", proj,
		"-f", composePath,
		"-f", m.overrideFile(id),
		"--env-file", m.envFile(id),
		"up", "-d", "--build", "--remove-orphans",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("compose up: %s: %w", out, err)
	}
	return nil
}

func (m *Manager) compose(ctx context.Context, id, composeFile string, args ...string) ([]byte, error) {
	composePath := filepath.Join(m.repoDir(id), composeFile)
	fullArgs := []string{
		"compose",
		"-p", ProjectName(id),
		"-f", composePath,
		"-f", m.overrideFile(id),
	}
	fullArgs = append(fullArgs, args...)
	return exec.CommandContext(ctx, "docker", fullArgs...).CombinedOutput()
}

func cloneRepo(ctx context.Context, repoURL, token, dir, branch string) error {
	authURL := repoURLWithToken(repoURL, token)
	out, err := exec.CommandContext(ctx, "git", "clone",
		"--depth=1", "--branch", branch,
		authURL, dir,
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w", out, err)
	}
	return nil
}

func pullRepo(ctx context.Context, dir, branch, token string) error {
	// Update remote URL with fresh token in case it expired
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "fetch", "--depth=1", "origin", branch).CombinedOutput()
	if err != nil {
		return fmt.Errorf("fetch: %s: %w", out, err)
	}
	out, err = exec.CommandContext(ctx, "git", "-C", dir, "reset", "--hard", "origin/"+branch).CombinedOutput()
	if err != nil {
		return fmt.Errorf("reset: %s: %w", out, err)
	}
	return nil
}

func getHeadSHA(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func repoURLWithToken(repoURL, token string) string {
	if token == "" {
		return repoURL
	}
	// Convert https://github.com/owner/repo to https://x-access-token:TOKEN@github.com/owner/repo
	return strings.Replace(repoURL, "https://", "https://x-access-token:"+token+"@", 1)
}

func writeEnvFile(path string, env map[string]string) error {
	var sb strings.Builder
	for k, v := range env {
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(v)
		sb.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(sb.String()), 0600)
}

type resourceLimit struct {
	CPUs   string `yaml:"cpus"`
	Memory string `yaml:"memory"`
}

type deployResources struct {
	Limits resourceLimit `yaml:"limits"`
}

type deployConfig struct {
	Resources deployResources `yaml:"resources"`
}

type serviceOverride struct {
	Deploy deployConfig `yaml:"deploy"`
}

type composeOverride struct {
	Services map[string]serviceOverride `yaml:"services"`
}

func writeOverride(path string, services []string, limits models.ResourceLimits) error {
	n := len(services)
	if n == 0 {
		n = 1
	}

	cpuPerService := limits.CPULimit / float64(n)
	if cpuPerService < 0.1 {
		cpuPerService = 0.1
	}
	memPerService := limits.MemoryLimitMb / n
	if memPerService < 64 {
		memPerService = 64
	}

	override := composeOverride{
		Services: make(map[string]serviceOverride, len(services)),
	}
	for _, svc := range services {
		override.Services[svc] = serviceOverride{
			Deploy: deployConfig{
				Resources: deployResources{
					Limits: resourceLimit{
						CPUs:   strconv.FormatFloat(cpuPerService, 'f', 2, 64),
						Memory: strconv.Itoa(memPerService) + "m",
					},
				},
			},
		}
	}

	data, err := yaml.Marshal(override)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func serviceFromContainerName(name, project string) string {
	name = strings.TrimPrefix(name, "/")
	name = strings.TrimPrefix(name, project+"-")
	// Strip trailing replica number: web-1 → web
	parts := strings.Split(name, "-")
	if len(parts) > 1 {
		if _, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
			name = strings.Join(parts[:len(parts)-1], "-")
		}
	}
	return name
}
