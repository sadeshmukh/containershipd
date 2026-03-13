package store

import (
	"database/sql"
	"time"

	"github.com/sadeshmukh/containershipd/models"
)

type Metrics struct {
	db *sql.DB
}

func NewMetrics(db *sql.DB) *Metrics {
	return &Metrics{db: db}
}

func (s *Metrics) Insert(deploymentID string, svcs []models.ServiceMetrics) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := nowStr()
	for _, svc := range svcs {
		if _, err := tx.Exec(`
			INSERT INTO metrics (deployment_id, service_name, cpu_percent, memory_mb, network_rx_mb, network_tx_mb, recorded_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			deploymentID, svc.Name, svc.CPUPercent, svc.MemoryMb, svc.NetworkRxMb, svc.NetworkTxMb, now,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Latest returns the most recent metric snapshot for a deployment.
func (s *Metrics) Latest(deploymentID string) (*models.DeploymentMetrics, error) {
	rows, err := s.db.Query(`
		SELECT service_name, cpu_percent, memory_mb, network_rx_mb, network_tx_mb, recorded_at
		FROM metrics
		WHERE deployment_id = ?
		  AND recorded_at = (SELECT MAX(recorded_at) FROM metrics WHERE deployment_id = ?)`,
		deploymentID, deploymentID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	dm := &models.DeploymentMetrics{DeploymentID: deploymentID}
	for rows.Next() {
		var svc models.ServiceMetrics
		var ts string
		if err := rows.Scan(&svc.Name, &svc.CPUPercent, &svc.MemoryMb, &svc.NetworkRxMb, &svc.NetworkTxMb, &ts); err != nil {
			return nil, err
		}
		dm.Timestamp = parseTime(ts)
		dm.Services = append(dm.Services, svc)
	}
	return dm, rows.Err()
}

// History returns metrics within a time range, aggregated by resolution.
func (s *Metrics) History(deploymentID string, from, to time.Time) ([]models.DeploymentMetrics, error) {
	rows, err := s.db.Query(`
		SELECT service_name, cpu_percent, memory_mb, network_rx_mb, network_tx_mb, recorded_at
		FROM metrics
		WHERE deployment_id = ? AND recorded_at >= ? AND recorded_at <= ?
		ORDER BY recorded_at ASC`,
		deploymentID, from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Group by timestamp
	byTS := map[string]*models.DeploymentMetrics{}
	var order []string

	for rows.Next() {
		var svc models.ServiceMetrics
		var ts string
		if err := rows.Scan(&svc.Name, &svc.CPUPercent, &svc.MemoryMb, &svc.NetworkRxMb, &svc.NetworkTxMb, &ts); err != nil {
			return nil, err
		}
		if _, ok := byTS[ts]; !ok {
			byTS[ts] = &models.DeploymentMetrics{
				DeploymentID: deploymentID,
				Timestamp:    parseTime(ts),
			}
			order = append(order, ts)
		}
		byTS[ts].Services = append(byTS[ts].Services, svc)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := make([]models.DeploymentMetrics, 0, len(order))
	for _, ts := range order {
		result = append(result, *byTS[ts])
	}
	return result, nil
}

// Purge removes metrics older than 24 hours.
func (s *Metrics) Purge() error {
	cutoff := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	_, err := s.db.Exec(`DELETE FROM metrics WHERE recorded_at < ?`, cutoff)
	return err
}
