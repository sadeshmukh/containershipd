package compose

import (
	"context"
	"encoding/json"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/sadeshmukh/containershipd/models"
	"github.com/sadeshmukh/containershipd/store"
)

// Collector polls Docker stats for all running deployments on a fixed interval.
type Collector struct {
	deployments *store.Deployments
	metrics     *store.Metrics
	interval    time.Duration
}

func NewCollector(deployments *store.Deployments, metrics *store.Metrics) *Collector {
	return &Collector{
		deployments: deployments,
		metrics:     metrics,
		interval:    30 * time.Second,
	}
}

func (c *Collector) Run(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	// Purge old metrics every hour.
	purgeTicker := time.NewTicker(time.Hour)
	defer purgeTicker.Stop()

	for {
		select {
		case <-ticker.C:
			c.collect(ctx)
		case <-purgeTicker.C:
			if err := c.metrics.Purge(); err != nil {
				slog.Warn("metrics purge failed", "error", err)
			}
		case <-ctx.Done():
			return
		}
	}
}

type dockerStatsJSON struct {
	Name     string `json:"Name"`
	CPUPerc  string `json:"CPUPerc"`
	MemUsage string `json:"MemUsage"`
	NetIO    string `json:"NetIO"`
}

func (c *Collector) collect(ctx context.Context) {
	deployments, err := c.deployments.ListByStatus(models.StatusRunning)
	if err != nil {
		slog.Warn("metrics collector: list deployments failed", "error", err)
		return
	}

	for _, d := range deployments {
		// Per-deployment timeout so one slow/hung container can't stall the whole loop.
		dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		svcs, err := collectForDeployment(dctx, d)
		cancel()
		if err != nil {
			slog.Warn("metrics collect failed", "deployment", d.ID, "error", err)
			continue
		}
		if len(svcs) == 0 {
			continue
		}
		if err := c.metrics.Insert(d.ID, svcs); err != nil {
			slog.Warn("metrics insert failed", "deployment", d.ID, "error", err)
		}
	}
}

func collectForDeployment(ctx context.Context, d *models.Deployment) ([]models.ServiceMetrics, error) {
	proj := ProjectName(d.ID)

	// Get running container IDs for this compose project.
	out, err := exec.CommandContext(ctx, "docker", "compose", "-p", proj, "ps", "-q").Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return nil, nil
	}

	ids := strings.Fields(strings.TrimSpace(string(out)))

	// docker stats --no-stream outputs one JSON object per line.
	args := append([]string{"stats", "--no-stream", "--format", "{{json .}}"}, ids...)
	statsOut, err := exec.CommandContext(ctx, "docker", args...).Output()
	if err != nil {
		return nil, err
	}

	var svcs []models.ServiceMetrics
	for _, line := range strings.Split(strings.TrimSpace(string(statsOut)), "\n") {
		if line == "" {
			continue
		}
		var s dockerStatsJSON
		if err := json.Unmarshal([]byte(line), &s); err != nil {
			continue
		}
		svcs = append(svcs, models.ServiceMetrics{
			Name:        serviceFromContainerName(s.Name, proj),
			CPUPercent:  parsePercent(s.CPUPerc),
			MemoryMb:    parseMemMb(s.MemUsage),
			NetworkRxMb: parseNetMb(s.NetIO, 0),
			NetworkTxMb: parseNetMb(s.NetIO, 1),
		})
	}
	return svcs, nil
}

// parsePercent converts "2.34%" → 2.34.
func parsePercent(s string) float64 {
	s = strings.TrimSuffix(strings.TrimSpace(s), "%")
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

// parseMemMb converts "128MiB / 512MiB" → 128.0 (first value, in MiB).
func parseMemMb(s string) float64 {
	parts := strings.SplitN(s, "/", 2)
	return parseSizeMb(strings.TrimSpace(parts[0]))
}

// parseNetMb extracts rx (index=0) or tx (index=1) from "1.2kB / 3.4MB".
func parseNetMb(s string, idx int) float64 {
	parts := strings.SplitN(s, "/", 2)
	if idx >= len(parts) {
		return 0
	}
	return parseSizeMb(strings.TrimSpace(parts[idx]))
}

// parseSizeMb converts human-readable sizes (kB, MB, MiB, GB, GiB) to MiB float.
func parseSizeMb(s string) float64 {
	s = strings.TrimSpace(s)
	multipliers := []struct {
		suffix string
		factor float64
	}{
		{"GiB", 1024},
		{"MiB", 1},
		{"KiB", 1.0 / 1024},
		{"GB", 953.674},
		{"MB", 0.953674},
		{"kB", 0.000953674},
		{"B", 1.0 / (1024 * 1024)},
	}
	for _, m := range multipliers {
		if strings.HasSuffix(s, m.suffix) {
			v, _ := strconv.ParseFloat(strings.TrimSuffix(s, m.suffix), 64)
			return v * m.factor
		}
	}
	v, _ := strconv.ParseFloat(s, 64)
	return v
}
