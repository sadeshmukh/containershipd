package compose

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/sadeshmukh/containershipd/models"
)

// serviceNameRE matches valid Docker Compose service names.
var serviceNameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// validateRepoURL ensures the URL is a plain https:// GitHub URL with no
// embedded credentials or unexpected characters that could inject git flags.
func validateRepoURL(u string) error {
	if !strings.HasPrefix(u, "https://github.com/") {
		return fmt.Errorf("repo URL must start with https://github.com/")
	}
	// Reject embedded userinfo (e.g. https://user:pass@github.com/...)
	if strings.Contains(u[len("https://"):], "@") {
		return fmt.Errorf("repo URL must not contain credentials")
	}
	// Reject newlines or null bytes that could escape shell arguments.
	if strings.ContainsAny(u, "\n\r\x00") {
		return fmt.Errorf("repo URL contains invalid characters")
	}
	return nil
}

// Manager manages Docker Compose deployments on disk.
type Manager struct {
	dataDir    string
	baseDomain string
}

func NewManager(dataDir, baseDomain string) *Manager {
	return &Manager{dataDir: dataDir, baseDomain: baseDomain}
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

// sanitizedPath is the port-stripped copy of the user's compose file.
func (m *Manager) sanitizedPath(id string) string {
	return filepath.Join(m.deployDir(id), "docker-compose.sanitized.yml")
}

func (m *Manager) nginxConfPath(id string) string {
	return filepath.Join(m.deployDir(id), "nginx.conf")
}

func (m *Manager) envFile(id string) string {
	return filepath.Join(m.deployDir(id), ".env")
}

// composePath returns the absolute path to the compose file in the repo,
// validated to be within the repo directory.
func (m *Manager) composePath(id, composeFile string) (string, error) {
	repoDir := m.repoDir(id)
	p := filepath.Join(repoDir, composeFile)
	rel, err := filepath.Rel(repoDir, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("compose file %q is outside repository directory", composeFile)
	}
	return p, nil
}

// Deploy clones the repo, applies resource limits, and starts all services.
// Returns the deployed commit SHA.
func (m *Manager) Deploy(ctx context.Context, d *models.Deployment, githubToken string) (string, error) {
	if err := validateRepoURL(d.Github.RepoURL); err != nil {
		return "", err
	}
	if err := os.MkdirAll(m.deployDir(d.ID), 0700); err != nil {
		return "", err
	}

	repoDir := m.repoDir(d.ID)

	if _, err := os.Stat(repoDir); os.IsNotExist(err) {
		if err := cloneRepo(ctx, d.Github.RepoURL, githubToken, repoDir, d.Github.Branch); err != nil {
			return "", fmt.Errorf("clone failed: %w", err)
		}
	} else {
		if err := pullRepo(ctx, repoDir, d.Github.RepoURL, d.Github.Branch, githubToken); err != nil {
			slog.Warn("git pull failed, using existing code", "deployment", d.ID, "error", err)
		}
	}

	sha, err := getHeadSHA(ctx, repoDir)
	if err != nil {
		return "", err
	}

	composePath, err := m.composePath(d.ID, d.Github.ComposeFile)
	if err != nil {
		return "", err
	}
	if err := sanitizeComposeFile(composePath, m.sanitizedPath(d.ID)); err != nil {
		return "", fmt.Errorf("sanitize compose file: %w", err)
	}
	services, err := ServiceNames(m.sanitizedPath(d.ID))
	if err != nil {
		return "", fmt.Errorf("invalid compose file: %w", err)
	}

	if err := writeEnvFile(m.envFile(d.ID), d.Env); err != nil {
		return "", err
	}

	if err := m.writeDeploymentOverride(d, services); err != nil {
		return "", err
	}

	if err := m.composeUp(ctx, d.ID); err != nil {
		return "", err
	}

	return sha, nil
}

// Redeploy pulls latest code and restarts.
func (m *Manager) Redeploy(ctx context.Context, d *models.Deployment, githubToken string) (string, error) {
	if err := validateRepoURL(d.Github.RepoURL); err != nil {
		return "", err
	}
	repoDir := m.repoDir(d.ID)
	if err := pullRepo(ctx, repoDir, d.Github.RepoURL, d.Github.Branch, githubToken); err != nil {
		return "", fmt.Errorf("pull failed: %w", err)
	}

	sha, err := getHeadSHA(ctx, repoDir)
	if err != nil {
		return "", err
	}

	composePath, err := m.composePath(d.ID, d.Github.ComposeFile)
	if err != nil {
		return "", err
	}
	if err := sanitizeComposeFile(composePath, m.sanitizedPath(d.ID)); err != nil {
		return "", fmt.Errorf("sanitize compose file: %w", err)
	}
	services, err := ServiceNames(m.sanitizedPath(d.ID))
	if err != nil {
		return "", err
	}

	if err := writeEnvFile(m.envFile(d.ID), d.Env); err != nil {
		return "", err
	}

	if err := m.writeDeploymentOverride(d, services); err != nil {
		return "", err
	}

	if err := m.composeUp(ctx, d.ID); err != nil {
		return "", err
	}

	return sha, nil
}

// Reconfigure applies updated resource limits or env without pulling new code.
func (m *Manager) Reconfigure(ctx context.Context, d *models.Deployment) error {
	// Re-sanitize in case the compose file changed (e.g. new service added).
	composePath, err := m.composePath(d.ID, d.Github.ComposeFile)
	if err != nil {
		return err
	}
	if err := sanitizeComposeFile(composePath, m.sanitizedPath(d.ID)); err != nil {
		return fmt.Errorf("sanitize compose file: %w", err)
	}
	services, err := ServiceNames(m.sanitizedPath(d.ID))
	if err != nil {
		return err
	}

	if err := writeEnvFile(m.envFile(d.ID), d.Env); err != nil {
		return err
	}

	if err := m.writeDeploymentOverride(d, services); err != nil {
		return err
	}

	return m.composeUp(ctx, d.ID)
}

// Start starts a stopped deployment.
func (m *Manager) Start(ctx context.Context, d *models.Deployment) error {
	return m.composeUp(ctx, d.ID)
}

// Stop stops all services without removing volumes.
func (m *Manager) Stop(ctx context.Context, d *models.Deployment) error {
	out, err := m.compose(ctx, d.ID, "stop")
	if err != nil {
		return fmt.Errorf("compose stop: %s: %w", out, err)
	}
	return nil
}

// Restart restarts all services.
func (m *Manager) Restart(ctx context.Context, d *models.Deployment) error {
	out, err := m.compose(ctx, d.ID, "restart")
	if err != nil {
		return fmt.Errorf("compose restart: %s: %w", out, err)
	}
	return nil
}

// Teardown stops and removes all containers, networks, and volumes.
func (m *Manager) Teardown(ctx context.Context, d *models.Deployment) error {
	if _, err := os.Stat(m.sanitizedPath(d.ID)); err == nil {
		out, err := m.compose(ctx, d.ID, "down", "--volumes", "--remove-orphans")
		if err != nil {
			// Log but don't fail — containers may already be gone.
			slog.Warn("compose down had errors", "deployment", d.ID, "output", string(out))
		}
	}
	return os.RemoveAll(m.deployDir(d.ID))
}

// Logs returns a reader for streaming logs. Caller must close it.
func (m *Manager) Logs(ctx context.Context, d *models.Deployment, service string, follow bool) (io.ReadCloser, error) {
	if service != "" && !serviceNameRE.MatchString(service) {
		return nil, fmt.Errorf("invalid service name %q", service)
	}
	args := []string{
		"compose",
		"-p", ProjectName(d.ID),
		"--project-directory", m.repoDir(d.ID),
		"-f", m.sanitizedPath(d.ID),
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

// ---- internal helpers ----

// writeDeploymentOverride generates the override file (resource limits + optional
// nginx sidecar for Traefik routing) and the nginx.conf when proxy is configured.
func (m *Manager) writeDeploymentOverride(d *models.Deployment, services []string) error {
	return writeOverride(
		m.overrideFile(d.ID),
		m.nginxConfPath(d.ID),
		services,
		d.ResourceLimits,
		d.Proxy,
		m.baseDomain,
		d.ID,
	)
}

func (m *Manager) composeUp(ctx context.Context, id string) error {
	proj := ProjectName(id)
	args := []string{
		"compose",
		"-p", proj,
		"--project-directory", m.repoDir(id),
		"-f", m.sanitizedPath(id),
	}
	if _, err := os.Stat(m.overrideFile(id)); err == nil {
		args = append(args, "-f", m.overrideFile(id))
	}
	args = append(args, "--env-file", m.envFile(id), "up", "-d", "--build", "--remove-orphans")
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("compose up: %s: %w", out, err)
	}
	return nil
}

func (m *Manager) compose(ctx context.Context, id string, args ...string) ([]byte, error) {
	fullArgs := []string{
		"compose",
		"-p", ProjectName(id),
		"--project-directory", m.repoDir(id),
		"-f", m.sanitizedPath(id),
	}
	if _, err := os.Stat(m.overrideFile(id)); err == nil {
		fullArgs = append(fullArgs, "-f", m.overrideFile(id))
	}
	fullArgs = append(fullArgs, args...)
	return exec.CommandContext(ctx, "docker", fullArgs...).CombinedOutput()
}

// sanitizeComposeFile reads srcPath, strips all `ports` from every service,
// and writes the result to dstPath. This ensures no containers publish ports
// directly to the host — all external traffic must flow through Traefik.
func sanitizeComposeFile(srcPath, dstPath string) error {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}

	var cf map[string]any
	if err := yaml.Unmarshal(data, &cf); err != nil {
		return err
	}

	if services, ok := cf["services"].(map[string]any); ok {
		for name, svcAny := range services {
			if svc, ok := svcAny.(map[string]any); ok {
				delete(svc, "ports")
				delete(svc, "container_name") // prevent cross-deployment name conflicts
				services[name] = svc
			}
		}
	}

	out, err := yaml.Marshal(cf)
	if err != nil {
		return err
	}
	return os.WriteFile(dstPath, out, 0600)
}

func cloneRepo(ctx context.Context, repoURL, token, dir, branch string) error {
	authURL := repoURLWithToken(repoURL, token)
	out, err := exec.CommandContext(ctx, "git", "clone",
		"--depth=1", "--branch", branch,
		authURL, dir,
	).CombinedOutput()
	if err != nil {
		// Scrub the token from error output before propagating.
		msg := string(out)
		if token != "" {
			msg = strings.ReplaceAll(msg, token, "<redacted>")
		}
		return fmt.Errorf("%s: %w", msg, err)
	}
	// Rewrite the remote URL to strip the token so it is not stored on disk
	// in .git/config for the lifetime of the deployment.
	if token != "" {
		if out, err := exec.CommandContext(ctx, "git", "-C", dir, "remote", "set-url", "origin", repoURL).CombinedOutput(); err != nil {
			return fmt.Errorf("strip remote token: %s: %w", out, err)
		}
	}
	return nil
}

func pullRepo(ctx context.Context, dir, repoURL, branch, token string) error {
	// Pass the token via a temporary per-command config override so it is
	// never written to .git/config on disk.
	fetchArgs := []string{"-C", dir}
	if token != "" {
		fetchArgs = append(fetchArgs, "-c", "remote.origin.url="+repoURLWithToken(repoURL, token))
	}
	fetchArgs = append(fetchArgs, "fetch", "--depth=1", "origin", branch)
	out, err := exec.CommandContext(ctx, "git", fetchArgs...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("fetch: %s: %w", out, err)
	}
	out, err = exec.CommandContext(ctx, "git", "-C", dir, "reset", "--hard", "origin/"+branch).CombinedOutput()
	if err != nil {
		return fmt.Errorf("reset: %s: %w", out, err)
	}
	return nil
}

func getHeadSHA(ctx context.Context, dir string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD").Output()
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
		if strings.ContainsAny(k, "=\n\r\x00") {
			return fmt.Errorf("invalid env var key %q", k)
		}
		if strings.ContainsAny(v, "\n\r\x00") {
			return fmt.Errorf("invalid env var value for key %q", k)
		}
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(v)
		sb.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(sb.String()), 0600)
}

// ---- override YAML types ----

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
	Image      string            `yaml:"image,omitempty"`
	Networks   []string          `yaml:"networks,omitempty"`
	Volumes    []string          `yaml:"volumes,omitempty"`
	Labels     []string          `yaml:"labels,omitempty"`
	Restart    string            `yaml:"restart,omitempty"`
	Deploy     *deployConfig     `yaml:"deploy,omitempty"`
	StorageOpt map[string]string `yaml:"storage_opt,omitempty"`
}

type networkDef struct {
	External bool   `yaml:"external,omitempty"`
	Name     string `yaml:"name,omitempty"`
}

type composeOverride struct {
	Services map[string]serviceOverride `yaml:"services"`
	Networks map[string]networkDef      `yaml:"networks,omitempty"`
}

func writeOverride(
	path, nginxConfPath string,
	services []string,
	limits models.ResourceLimits,
	proxy *models.ProxyConfig,
	baseDomain, deploymentID string,
) error {
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
	storageMbPerService := int(limits.StorageLimitGb * 1024 / float64(n))
	if limits.StorageLimitGb > 0 && storageMbPerService < 512 {
		storageMbPerService = 512
	}

	override := composeOverride{
		Services: make(map[string]serviceOverride, len(services)+1),
	}
	for _, name := range services {
		so := serviceOverride{
			Deploy: &deployConfig{
				Resources: deployResources{
					Limits: resourceLimit{
						CPUs:   strconv.FormatFloat(cpuPerService, 'f', 2, 64),
						Memory: strconv.Itoa(memPerService) + "m",
					},
				},
			},
		}
		if limits.StorageLimitGb > 0 {
			so.StorageOpt = map[string]string{
				"size": strconv.Itoa(storageMbPerService) + "m",
			}
		}
		override.Services[name] = so
	}

	// Inject nginx sidecar when proxy is configured and a base domain is set.
	if proxy != nil && baseDomain != "" {
		svc := proxy.Service
		if svc == "" {
			svc = "web"
		}
		port := proxy.Port
		if port == 0 {
			port = 80
		}

		if err := writeNginxConf(nginxConfPath, svc, port); err != nil {
			return fmt.Errorf("write nginx conf: %w", err)
		}

		traefikID := "csd" + strings.ReplaceAll(deploymentID, "-", "")
		fqdn := proxy.Subdomain + "." + baseDomain

		override.Services["csd-sidecar"] = serviceOverride{
			Image:   "nginx:alpine",
			Restart: "unless-stopped",
			Networks: []string{"default", "csd-traefik"},
			Volumes:  []string{nginxConfPath + ":/etc/nginx/nginx.conf:ro"},
			Labels: []string{
				"traefik.enable=true",
				"traefik.docker.network=csd-traefik",
				"traefik.http.routers." + traefikID + ".rule=Host(`" + fqdn + "`)",
				"traefik.http.routers." + traefikID + ".entrypoints=websecure",
				"traefik.http.routers." + traefikID + ".tls=true",
				"traefik.http.routers." + traefikID + ".tls.certresolver=letsencrypt",
				"traefik.http.services." + traefikID + ".loadbalancer.server.port=80",
			},
			Deploy: &deployConfig{
				Resources: deployResources{
					Limits: resourceLimit{CPUs: "0.10", Memory: "64m"},
				},
			},
		}

		override.Networks = map[string]networkDef{
			"csd-traefik": {External: true, Name: "csd-traefik"},
		}
	}

	data, err := yaml.Marshal(override)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// writeNginxConf generates an nginx reverse-proxy config that forwards all
// traffic to targetService:targetPort within the Compose network.
func writeNginxConf(path, targetService string, targetPort int) error {
	content := fmt.Sprintf(`events {
    worker_connections 64;
}
http {
    server {
        listen 80;
        server_name _;
        location / {
            proxy_pass         http://%s:%d;
            proxy_http_version 1.1;
            proxy_set_header   Upgrade $http_upgrade;
            proxy_set_header   Connection "upgrade";
            proxy_set_header   Host $host;
            proxy_set_header   X-Real-IP $remote_addr;
            proxy_set_header   X-Forwarded-For $proxy_add_x_forwarded_for;
            proxy_set_header   X-Forwarded-Proto $scheme;
            proxy_buffering    off;
        }
    }
}
`, targetService, targetPort)
	return os.WriteFile(path, []byte(content), 0600)
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
