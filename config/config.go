package config

import (
	"log/slog"
	"os"
	"strings"
)

type Config struct {
	ListenAddr     string
	AdminSecret    string
	EncryptionKey  [32]byte
	DatabasePath   string
	DataDir        string
	JWTSecret      string
	WebhookBaseURL string
}

func Load() *Config {
	cfg := &Config{
		ListenAddr:     getEnv("LISTEN_ADDR", ":8080"),
		AdminSecret:    requireEnv("ADMIN_SECRET"),
		DatabasePath:   getEnv("DATABASE_PATH", "/var/lib/containershipd/containershipd.db"),
		DataDir:        getEnv("DATA_DIR", "/var/lib/containershipd"),
		JWTSecret:      requireEnv("JWT_SECRET"),
		WebhookBaseURL: getEnv("WEBHOOK_BASE_URL", ""),
	}

	encKey := requireEnv("ENCRYPTION_KEY")
	if len(encKey) < 32 {
		slog.Error("ENCRYPTION_KEY must be at least 32 characters")
		os.Exit(1)
	}
	copy(cfg.EncryptionKey[:], []byte(encKey)[:32])

	return cfg
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return strings.TrimSpace(v)
	}
	return fallback
}

func requireEnv(key string) string {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		slog.Error("required environment variable not set", "key", key)
		os.Exit(1)
	}
	return strings.TrimSpace(v)
}
