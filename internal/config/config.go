package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr         string
	ShutdownTimeout  time.Duration
	DatabaseURL      string
	SeedPath         string
	ChronotopeURL    string
	RuntimeTokenEnv  string
	AdminTokenEnv    string
	InternalTokenEnv string
	SlackAPIURL      string
	PollInterval     time.Duration
}

func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:         envOrDefault("BOTS_HTTP_ADDR", "127.0.0.1:8090"),
		DatabaseURL:      strings.TrimSpace(os.Getenv("BOTS_DATABASE_URL")),
		SeedPath:         envOrDefault("BOTS_SEED_PATH", "configs/bot.yaml"),
		ChronotopeURL:    envOrDefault("BOTS_CHRONOTOPE_URL", "http://127.0.0.1:8080"),
		RuntimeTokenEnv:  envOrDefault("BOTS_RUNTIME_TOKEN_ENV", "BOTS_CHRONOTOPE_TOKEN"),
		AdminTokenEnv:    envOrDefault("BOTS_ADMIN_TOKEN_ENV", "BOTS_ADMIN_TOKEN"),
		InternalTokenEnv: envOrDefault("BOTS_INTERNAL_TOKEN_ENV", "APPLICATION_SERVICE_TOKEN"),
		SlackAPIURL:      envOrDefault("BOTS_SLACK_API_URL", "https://slack.com/api"),
	}
	_, port, err := net.SplitHostPort(cfg.HTTPAddr)
	if err != nil {
		return Config{}, fmt.Errorf("BOTS_HTTP_ADDR must be host:port: %w", err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 0 || portNumber > 65535 {
		return Config{}, fmt.Errorf("BOTS_HTTP_ADDR port must be between 0 and 65535")
	}
	cfg.ShutdownTimeout, err = time.ParseDuration(envOrDefault("BOTS_SHUTDOWN_TIMEOUT", "10s"))
	if err != nil || cfg.ShutdownTimeout <= 0 {
		return Config{}, fmt.Errorf("BOTS_SHUTDOWN_TIMEOUT must be a positive duration")
	}
	cfg.PollInterval, err = time.ParseDuration(envOrDefault("BOTS_POLL_INTERVAL", "1s"))
	if err != nil || cfg.PollInterval < 50*time.Millisecond || cfg.PollInterval > time.Minute {
		return Config{}, fmt.Errorf("BOTS_POLL_INTERVAL must be between 50ms and 1m")
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("BOTS_DATABASE_URL is required")
	}
	for _, ref := range []string{cfg.RuntimeTokenEnv, cfg.AdminTokenEnv, cfg.InternalTokenEnv} {
		if !environmentName.MatchString(ref) {
			return Config{}, fmt.Errorf("service credential configuration must use environment variable references")
		}
	}
	return cfg, nil
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
