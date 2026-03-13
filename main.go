package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/sadeshmukh/containershipd/api"
	"github.com/sadeshmukh/containershipd/compose"
	"github.com/sadeshmukh/containershipd/config"
	"github.com/sadeshmukh/containershipd/crypto"
	"github.com/sadeshmukh/containershipd/db"
	"github.com/sadeshmukh/containershipd/ghclient"
	"github.com/sadeshmukh/containershipd/store"
)

func main() {
	cfg := config.Load()

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	// Ensure required directories exist.
	for _, dir := range []string{
		filepath.Join(cfg.DataDir, "deployments"),
		filepath.Dir(cfg.DatabasePath),
	} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			slog.Error("failed to create directory", "path", dir, "error", err)
			os.Exit(1)
		}
	}

	database, err := db.Open(cfg.DatabasePath)
	if err != nil {
		slog.Error("failed to open database", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	if err := db.Migrate(database); err != nil {
		slog.Error("migration failed", "error", err)
		os.Exit(1)
	}

	enc := crypto.New(cfg.EncryptionKey)
	users := store.NewUsers(database)
	deployments := store.NewDeployments(database, enc)
	metrics := store.NewMetrics(database)
	composer := compose.NewManager(cfg.DataDir)
	collector := compose.NewCollector(deployments, metrics)
	ghClient := ghclient.New()

	go collector.Run(context.Background())

	router := api.NewRouter(cfg, users, deployments, metrics, composer, ghClient)

	srv := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      router,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0, // Disabled: WebSocket and streaming log handlers need unlimited write time.
		IdleTimeout:  120 * time.Second,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		slog.Info("containershipd started", "addr", cfg.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	<-quit
	slog.Info("shutting down")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("shutdown error", "error", err)
	}
}
