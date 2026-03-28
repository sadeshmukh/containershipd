package store

import (
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/sadeshmukh/containershipd/crypto"
	"github.com/sadeshmukh/containershipd/models"
)

const deploymentCols = `id, user_id, name, status, error_message,
    github_repo_url, github_branch, github_compose_file,
    github_commit_sha, github_webhook_id, github_auto_redeploy,
    github_token_enc,
    resource_cpu_limit, resource_memory_limit_mb, resource_storage_limit_gb,
    env_enc, webhook_secret,
    proxy_subdomain, proxy_service, proxy_port,
    created_at, updated_at, last_deployed_at`

type Deployments struct {
	db  *sql.DB
	enc *crypto.Crypto
}

func NewDeployments(db *sql.DB, enc *crypto.Crypto) *Deployments {
	return &Deployments{db: db, enc: enc}
}

type CreateDeploymentParams struct {
	UserID         string
	Name           string
	RepoURL        string
	Branch         string
	ComposeFile    string
	GithubToken    string
	AutoRedeploy   bool
	ResourceLimits models.ResourceLimits
	Env            map[string]string
	WebhookSecret  string
	Proxy          *models.ProxyConfig
}

func (s *Deployments) Create(p CreateDeploymentParams) (*models.Deployment, error) {
	id := uuid.New().String()
	now := nowStr()

	tokenEnc, err := s.enc.Encrypt(p.GithubToken)
	if err != nil {
		return nil, err
	}

	envJSON, err := json.Marshal(p.Env)
	if err != nil {
		return nil, err
	}
	envEnc, err := s.enc.Encrypt(string(envJSON))
	if err != nil {
		return nil, err
	}

	autoRedeploy := 0
	if p.AutoRedeploy {
		autoRedeploy = 1
	}

	proxySubdomain, proxyService, proxyPort := proxyValues(p.Proxy)

	_, err = s.db.Exec(`
		INSERT INTO deployments (
			id, user_id, name, status,
			github_repo_url, github_branch, github_compose_file,
			github_auto_redeploy, github_token_enc,
			resource_cpu_limit, resource_memory_limit_mb, resource_storage_limit_gb,
			env_enc, webhook_secret,
			proxy_subdomain, proxy_service, proxy_port,
			created_at, updated_at
		) VALUES (?, ?, ?, 'provisioning', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, p.UserID, p.Name,
		p.RepoURL, p.Branch, p.ComposeFile,
		autoRedeploy, tokenEnc,
		p.ResourceLimits.CPULimit, p.ResourceLimits.MemoryLimitMb, p.ResourceLimits.StorageLimitGb,
		envEnc, p.WebhookSecret,
		proxySubdomain, proxyService, proxyPort,
		now, now,
	)
	if err != nil {
		return nil, err
	}
	return s.Get(id)
}

func (s *Deployments) Get(id string) (*models.Deployment, error) {
	row := s.db.QueryRow(`SELECT `+deploymentCols+` FROM deployments WHERE id = ?`, id)
	d, err := s.scanDeployment(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if d.Env == nil {
		d.Env = map[string]string{}
	}
	return d, nil
}

func (s *Deployments) GetGithubToken(id string) (string, error) {
	var enc sql.NullString
	err := s.db.QueryRow(`SELECT github_token_enc FROM deployments WHERE id = ?`, id).Scan(&enc)
	if err != nil {
		return "", err
	}
	if !enc.Valid {
		return "", nil
	}
	return s.enc.Decrypt(enc.String)
}

type ListDeploymentsParams struct {
	UserID string
	Status string
	Limit  int
	Offset int
}

func (s *Deployments) List(p ListDeploymentsParams) ([]*models.Deployment, error) {
	if p.Limit == 0 {
		p.Limit = 50
	}
	query := `SELECT ` + deploymentCols + ` FROM deployments WHERE 1=1`
	args := []any{}
	if p.UserID != "" {
		query += ` AND user_id = ?`
		args = append(args, p.UserID)
	}
	if p.Status != "" {
		query += ` AND status = ?`
		args = append(args, p.Status)
	}
	query += ` ORDER BY created_at DESC LIMIT ? OFFSET ?`
	args = append(args, p.Limit, p.Offset)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	deployments := make([]*models.Deployment, 0)
	for rows.Next() {
		d, err := s.scanDeployment(rows)
		if err != nil {
			return nil, err
		}
		if d.Env == nil {
			d.Env = map[string]string{}
		}
		deployments = append(deployments, d)
	}
	return deployments, rows.Err()
}

func (s *Deployments) ListByStatus(status models.DeploymentStatus) ([]*models.Deployment, error) {
	return s.List(ListDeploymentsParams{Status: string(status), Limit: 1000})
}

func (s *Deployments) UpdateStatus(id string, status models.DeploymentStatus, errMsg string) error {
	_, err := s.db.Exec(
		`UPDATE deployments SET status = ?, error_message = ?, updated_at = ? WHERE id = ?`,
		status, errMsg, nowStr(), id,
	)
	return err
}

func (s *Deployments) SetDeployed(id, commitSha string) error {
	now := nowStr()
	_, err := s.db.Exec(
		`UPDATE deployments SET github_commit_sha = ?, last_deployed_at = ?, updated_at = ? WHERE id = ?`,
		commitSha, now, now, id,
	)
	return err
}

func (s *Deployments) SetWebhookID(id string, webhookID int64) error {
	_, err := s.db.Exec(
		`UPDATE deployments SET github_webhook_id = ?, updated_at = ? WHERE id = ?`,
		webhookID, nowStr(), id,
	)
	return err
}

// CheckSubdomainTaken reports whether the given subdomain is already in use
// by a deployment other than excludeID (pass "" to check without exclusion).
func (s *Deployments) CheckSubdomainTaken(subdomain, excludeID string) (bool, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM deployments WHERE proxy_subdomain = ? AND id != ?`,
		subdomain, excludeID,
	).Scan(&count)
	return count > 0, err
}

type UpdateDeploymentParams struct {
	Name           *string
	Branch         *string
	ComposeFile    *string
	AutoRedeploy   *bool
	ResourceLimits *models.ResourceLimits
	Env            map[string]string
	GithubToken    *string
	Proxy          *models.ProxyConfig
	ClearProxy     bool
}

func (s *Deployments) Update(id string, p UpdateDeploymentParams) (*models.Deployment, error) {
	now := nowStr()
	if p.Name != nil {
		if _, err := s.db.Exec(`UPDATE deployments SET name = ?, updated_at = ? WHERE id = ?`, *p.Name, now, id); err != nil {
			return nil, err
		}
	}
	if p.Branch != nil {
		if _, err := s.db.Exec(`UPDATE deployments SET github_branch = ?, updated_at = ? WHERE id = ?`, *p.Branch, now, id); err != nil {
			return nil, err
		}
	}
	if p.ComposeFile != nil {
		if _, err := s.db.Exec(`UPDATE deployments SET github_compose_file = ?, updated_at = ? WHERE id = ?`, *p.ComposeFile, now, id); err != nil {
			return nil, err
		}
	}
	if p.AutoRedeploy != nil {
		v := 0
		if *p.AutoRedeploy {
			v = 1
		}
		if _, err := s.db.Exec(`UPDATE deployments SET github_auto_redeploy = ?, updated_at = ? WHERE id = ?`, v, now, id); err != nil {
			return nil, err
		}
	}
	if p.ResourceLimits != nil {
		if _, err := s.db.Exec(`
			UPDATE deployments SET
				resource_cpu_limit = ?,
				resource_memory_limit_mb = ?,
				resource_storage_limit_gb = ?,
				updated_at = ?
			WHERE id = ?`,
			p.ResourceLimits.CPULimit, p.ResourceLimits.MemoryLimitMb,
			p.ResourceLimits.StorageLimitGb, now, id,
		); err != nil {
			return nil, err
		}
	}
	if p.Env != nil {
		envJSON, err := json.Marshal(p.Env)
		if err != nil {
			return nil, err
		}
		envEnc, err := s.enc.Encrypt(string(envJSON))
		if err != nil {
			return nil, err
		}
		if _, err := s.db.Exec(`UPDATE deployments SET env_enc = ?, updated_at = ? WHERE id = ?`, envEnc, now, id); err != nil {
			return nil, err
		}
	}
	if p.GithubToken != nil {
		tokenEnc, err := s.enc.Encrypt(*p.GithubToken)
		if err != nil {
			return nil, err
		}
		if _, err := s.db.Exec(`UPDATE deployments SET github_token_enc = ?, updated_at = ? WHERE id = ?`, tokenEnc, now, id); err != nil {
			return nil, err
		}
	}
	if p.Proxy != nil || p.ClearProxy {
		sub, svc, port := proxyValues(p.Proxy)
		if _, err := s.db.Exec(
			`UPDATE deployments SET proxy_subdomain = ?, proxy_service = ?, proxy_port = ?, updated_at = ? WHERE id = ?`,
			sub, svc, port, now, id,
		); err != nil {
			return nil, err
		}
	}
	return s.Get(id)
}

func (s *Deployments) Delete(id string) error {
	res, err := s.db.Exec(`DELETE FROM deployments WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Deployments) scanDeployment(sc scanner) (*models.Deployment, error) {
	var d models.Deployment
	var (
		errorMessage   sql.NullString
		commitSha      sql.NullString
		webhookID      sql.NullInt64
		githubTokenEnc sql.NullString
		envEnc         sql.NullString
		webhookSecret  sql.NullString
		proxySubdomain sql.NullString
		proxyService   sql.NullString
		proxyPort      sql.NullInt64
		lastDeployedAt sql.NullString
		autoRedeploy   int
		createdAt      string
		updatedAt      string
	)

	err := sc.Scan(
		&d.ID, &d.UserID, &d.Name, &d.Status, &errorMessage,
		&d.Github.RepoURL, &d.Github.Branch, &d.Github.ComposeFile,
		&commitSha, &webhookID, &autoRedeploy,
		&githubTokenEnc,
		&d.ResourceLimits.CPULimit, &d.ResourceLimits.MemoryLimitMb, &d.ResourceLimits.StorageLimitGb,
		&envEnc, &webhookSecret,
		&proxySubdomain, &proxyService, &proxyPort,
		&createdAt, &updatedAt, &lastDeployedAt,
	)
	if err != nil {
		return nil, err
	}

	if errorMessage.Valid {
		d.ErrorMessage = errorMessage.String
	}
	if commitSha.Valid {
		d.Github.CommitSha = commitSha.String
	}
	if webhookID.Valid {
		d.Github.WebhookID = webhookID.Int64
	}
	if webhookSecret.Valid {
		d.WebhookSecret = webhookSecret.String
	}
	d.Github.AutoRedeploy = autoRedeploy == 1

	if envEnc.Valid && envEnc.String != "" {
		plain, err := s.enc.Decrypt(envEnc.String)
		if err == nil {
			var env map[string]string
			if json.Unmarshal([]byte(plain), &env) == nil {
				d.Env = env
			}
		}
	}

	if proxySubdomain.Valid && proxySubdomain.String != "" {
		d.Proxy = &models.ProxyConfig{
			Subdomain: proxySubdomain.String,
			Service:   proxyService.String,
			Port:      int(proxyPort.Int64),
		}
	}

	d.CreatedAt = parseTime(createdAt)
	d.UpdatedAt = parseTime(updatedAt)
	if lastDeployedAt.Valid {
		t := parseTime(lastDeployedAt.String)
		d.LastDeployedAt = &t
	}

	return &d, nil
}

// proxyValues unpacks a *ProxyConfig into SQL-safe values (nil when proxy is nil).
func proxyValues(p *models.ProxyConfig) (subdomain, service any, port any) {
	if p == nil {
		return nil, nil, nil
	}
	svc := p.Service
	if svc == "" {
		svc = "web"
	}
	prt := p.Port
	if prt == 0 {
		prt = 80
	}
	return p.Subdomain, svc, prt
}

