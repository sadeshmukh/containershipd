package db

import (
	"database/sql"
	"strings"

	_ "modernc.org/sqlite"
)

func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path+"?_journal=WAL&_timeout=5000&_fk=true")
	if err != nil {
		return nil, err
	}
	// WAL mode supports one writer + many concurrent readers on separate connections.
	// SetMaxOpenConns(1) serialises ALL access through a single connection, causing
	// the metrics goroutine and API handlers to deadlock waiting on each other.
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	if err := db.Ping(); err != nil {
		return nil, err
	}
	return db, nil
}

func Migrate(db *sql.DB) error {
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	return applyColumnMigrations(db)
}

// applyColumnMigrations runs ALTER TABLE statements for columns added after
// the initial schema. Duplicate-column errors are silently ignored so the
// function is idempotent on existing databases.
func applyColumnMigrations(db *sql.DB) error {
	alters := []string{
		`ALTER TABLE deployments ADD COLUMN proxy_subdomain TEXT`,
		`ALTER TABLE deployments ADD COLUMN proxy_service TEXT`,
		`ALTER TABLE deployments ADD COLUMN proxy_port INTEGER`,
	}
	for _, stmt := range alters {
		if _, err := db.Exec(stmt); err != nil {
			if !strings.Contains(err.Error(), "duplicate column name") {
				return err
			}
		}
	}
	// Index must be created after the column exists.
	_, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_deployments_proxy_subdomain
		ON deployments(proxy_subdomain) WHERE proxy_subdomain IS NOT NULL`)
	return err
}

const schema = `
CREATE TABLE IF NOT EXISTS users (
    id                         TEXT PRIMARY KEY,
    external_id                TEXT UNIQUE NOT NULL,
    status                     TEXT NOT NULL DEFAULT 'active',
    quota_max_deployments      INTEGER NOT NULL DEFAULT 3,
    quota_max_cpu_cores        REAL NOT NULL DEFAULT 2.0,
    quota_max_memory_mb        INTEGER NOT NULL DEFAULT 2048,
    quota_max_storage_gb       REAL NOT NULL DEFAULT 10.0,
    quota_max_bandwidth_gb_month REAL NOT NULL DEFAULT 100.0,
    created_at                 TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at                 TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS deployments (
    id                        TEXT PRIMARY KEY,
    user_id                   TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name                      TEXT NOT NULL,
    status                    TEXT NOT NULL DEFAULT 'provisioning',
    error_message             TEXT,
    github_repo_url           TEXT NOT NULL,
    github_branch             TEXT NOT NULL DEFAULT 'main',
    github_compose_file       TEXT NOT NULL DEFAULT 'docker-compose.yml',
    github_commit_sha         TEXT,
    github_webhook_id         INTEGER,
    github_auto_redeploy      INTEGER NOT NULL DEFAULT 0,
    github_token_enc          TEXT,
    resource_cpu_limit        REAL NOT NULL DEFAULT 0.5,
    resource_memory_limit_mb  INTEGER NOT NULL DEFAULT 512,
    resource_storage_limit_gb REAL NOT NULL DEFAULT 2.0,
    env_enc                   TEXT,
    webhook_secret            TEXT,
    proxy_subdomain           TEXT,
    proxy_service             TEXT,
    proxy_port                INTEGER,
    created_at                TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at                TEXT NOT NULL DEFAULT (datetime('now')),
    last_deployed_at          TEXT
);

CREATE TABLE IF NOT EXISTS metrics (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    deployment_id  TEXT NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    service_name   TEXT NOT NULL,
    cpu_percent    REAL NOT NULL DEFAULT 0,
    memory_mb      REAL NOT NULL DEFAULT 0,
    network_rx_mb  REAL NOT NULL DEFAULT 0,
    network_tx_mb  REAL NOT NULL DEFAULT 0,
    recorded_at    TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_deployments_user_id  ON deployments(user_id);
CREATE INDEX IF NOT EXISTS idx_deployments_status   ON deployments(status);
CREATE INDEX IF NOT EXISTS idx_metrics_deployment   ON metrics(deployment_id);
CREATE INDEX IF NOT EXISTS idx_metrics_recorded_at  ON metrics(recorded_at);
`
