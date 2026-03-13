package store

import (
	"database/sql"
	"errors"

	"github.com/google/uuid"
	"github.com/sadeshmukh/containershipd/models"
)

var ErrNotFound = errors.New("not found")

const userCols = `id, external_id, status,
    quota_max_deployments, quota_max_cpu_cores, quota_max_memory_mb,
    quota_max_storage_gb, quota_max_bandwidth_gb_month,
    created_at, updated_at`

type Users struct {
	db *sql.DB
}

func NewUsers(db *sql.DB) *Users {
	return &Users{db: db}
}

type CreateUserParams struct {
	ExternalID string
	Quota      models.Quota
}

func (s *Users) Create(p CreateUserParams) (*models.User, error) {
	id := uuid.New().String()
	now := nowStr()
	_, err := s.db.Exec(`
		INSERT INTO users (id, external_id, status,
			quota_max_deployments, quota_max_cpu_cores, quota_max_memory_mb,
			quota_max_storage_gb, quota_max_bandwidth_gb_month,
			created_at, updated_at)
		VALUES (?, ?, 'active', ?, ?, ?, ?, ?, ?, ?)`,
		id, p.ExternalID,
		p.Quota.MaxDeployments, p.Quota.MaxCPUCores, p.Quota.MaxMemoryMb,
		p.Quota.MaxStorageGb, p.Quota.MaxBandwidthGbMonth,
		now, now,
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return nil, ErrConflict
		}
		return nil, err
	}
	return s.Get(id)
}

func (s *Users) Get(id string) (*models.User, error) {
	row := s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE id = ?`, id)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

func (s *Users) GetByExternalID(externalID string) (*models.User, error) {
	row := s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE external_id = ?`, externalID)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

type ListUsersParams struct {
	Status string
	Limit  int
	Offset int
}

func (s *Users) List(p ListUsersParams) ([]*models.User, error) {
	if p.Limit == 0 {
		p.Limit = 50
	}
	query := `SELECT ` + userCols + ` FROM users WHERE 1=1`
	args := []any{}
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

	var users []*models.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

type UpdateUserParams struct {
	Status     *string
	ExternalID *string
	Quota      *models.Quota
}

func (s *Users) Update(id string, p UpdateUserParams) (*models.User, error) {
	now := nowStr()
	if p.Status != nil {
		if _, err := s.db.Exec(
			`UPDATE users SET status = ?, updated_at = ? WHERE id = ?`,
			*p.Status, now, id,
		); err != nil {
			return nil, err
		}
	}
	if p.ExternalID != nil {
		if _, err := s.db.Exec(
			`UPDATE users SET external_id = ?, updated_at = ? WHERE id = ?`,
			*p.ExternalID, now, id,
		); err != nil {
			return nil, err
		}
	}
	if p.Quota != nil {
		if _, err := s.db.Exec(`
			UPDATE users SET
				quota_max_deployments = ?,
				quota_max_cpu_cores = ?,
				quota_max_memory_mb = ?,
				quota_max_storage_gb = ?,
				quota_max_bandwidth_gb_month = ?,
				updated_at = ?
			WHERE id = ?`,
			p.Quota.MaxDeployments, p.Quota.MaxCPUCores, p.Quota.MaxMemoryMb,
			p.Quota.MaxStorageGb, p.Quota.MaxBandwidthGbMonth, now, id,
		); err != nil {
			return nil, err
		}
	}
	return s.Get(id)
}

func (s *Users) Delete(id string) error {
	res, err := s.db.Exec(`DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Users) GetUsage(id string) (*models.Usage, error) {
	usage := &models.Usage{}
	err := s.db.QueryRow(`
		SELECT
			COUNT(*),
			COALESCE(SUM(resource_cpu_limit), 0),
			COALESCE(SUM(resource_memory_limit_mb), 0),
			COALESCE(SUM(resource_storage_limit_gb), 0)
		FROM deployments
		WHERE user_id = ? AND status NOT IN ('deleted', 'error')`,
		id,
	).Scan(&usage.Deployments, &usage.CPUCores, &usage.MemoryMb, &usage.StorageGb)
	return usage, err
}

func scanUser(s scanner) (*models.User, error) {
	var u models.User
	var createdAt, updatedAt string
	err := s.Scan(
		&u.ID, &u.ExternalID, &u.Status,
		&u.Quota.MaxDeployments, &u.Quota.MaxCPUCores, &u.Quota.MaxMemoryMb,
		&u.Quota.MaxStorageGb, &u.Quota.MaxBandwidthGbMonth,
		&createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}
	u.CreatedAt = parseTime(createdAt)
	u.UpdatedAt = parseTime(updatedAt)
	return &u, nil
}
