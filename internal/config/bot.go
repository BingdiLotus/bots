package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/bingdilotus/bots/internal/domain"
	"go.yaml.in/yaml/v3"
)

var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Seed is a first-import document. Runtime IDs, revisions and mutable database
// state are never accepted from the bootstrap file.
type Seed struct {
	TenantID       string           `json:"tenant_id"`
	ID             string           `json:"id"`
	Name           string           `json:"name"`
	Enabled        bool             `json:"enabled"`
	AccountEnabled bool             `json:"account_enabled"`
	Config         domain.BotConfig `json:"config"`
}

func LoadSeed(path string) (domain.Bot, error) {
	file, err := os.Open(path)
	if err != nil {
		return domain.Bot{}, fmt.Errorf("open bot configuration: %w", err)
	}
	defer file.Close()
	decoder := yaml.NewDecoder(io.LimitReader(file, 1<<20))
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return domain.Bot{}, fmt.Errorf("decode bot YAML: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return domain.Bot{}, fmt.Errorf("configuration must contain exactly one YAML document")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return domain.Bot{}, fmt.Errorf("configuration keys must be strings")
	}
	var seed Seed
	jsonDecoder := json.NewDecoder(bytes.NewReader(raw))
	jsonDecoder.DisallowUnknownFields()
	if err := jsonDecoder.Decode(&seed); err != nil {
		return domain.Bot{}, fmt.Errorf("invalid bot configuration: %w", err)
	}
	if strings.TrimSpace(seed.TenantID) == "" || strings.TrimSpace(seed.ID) == "" || strings.TrimSpace(seed.Name) == "" {
		return domain.Bot{}, fmt.Errorf("tenant_id, id and name are required")
	}
	cfg, err := NormalizeBot(seed.Config)
	if err != nil {
		return domain.Bot{}, err
	}
	return domain.Bot{TenantID: seed.TenantID, ID: seed.ID, Name: seed.Name, Enabled: seed.Enabled, AccountEnabled: seed.AccountEnabled, Config: cfg}, nil
}

func NormalizeBot(cfg domain.BotConfig) (domain.BotConfig, error) {
	if strings.TrimSpace(cfg.Model) == "" || strings.TrimSpace(cfg.Instructions) == "" || cfg.InstallationID == "" || cfg.ChannelID == "" || cfg.ChannelKind == "" || cfg.BotUserID == "" || cfg.BridgeRef == "" || cfg.TimerTargetRef == "" {
		return cfg, fmt.Errorf("model, instructions, channel account, route, bridge_ref and timer_target_ref are required")
	}
	if cfg.ChannelKind != "slack" {
		return cfg, fmt.Errorf("channel_kind is not supported by this deployment")
	}
	if cfg.Timezone == "" {
		cfg.Timezone = "Asia/Shanghai"
	}
	if _, err := time.LoadLocation(cfg.Timezone); err != nil {
		return cfg, fmt.Errorf("invalid organization timezone")
	}
	if !environmentName.MatchString(cfg.TokenEnv) || cfg.SigningSecretEnv != "" && !environmentName.MatchString(cfg.SigningSecretEnv) {
		return cfg, fmt.Errorf("credentials must be environment variable references")
	}
	if cfg.ChannelKind == "slack" && (cfg.WorkspaceID == "" || cfg.SigningSecretEnv == "") {
		return cfg, fmt.Errorf("Slack installation requires workspace_id and signing_secret_env")
	}
	if cfg.MaxSteps == 0 {
		cfg.MaxSteps = 16
	}
	if cfg.MaxOutputTokens == 0 {
		cfg.MaxOutputTokens = 4096
	}
	if cfg.MaxDurationMS == 0 {
		cfg.MaxDurationMS = 300000
	}
	if cfg.MaxPendingTasks == 0 {
		cfg.MaxPendingTasks = 20
	}
	if cfg.MaxSteps < 1 || cfg.MaxOutputTokens < 1 || cfg.MaxPendingTasks < 1 || cfg.MaxDurationMS < 1 || cfg.MaxDurationMS > int64((1<<63-1)/time.Millisecond) {
		return cfg, fmt.Errorf("execution and task limits must be positive and representable")
	}
	return cfg, nil
}

// Secret resolves a deployment reference. Errors deliberately omit its value.
func Secret(name string) (string, error) {
	if !environmentName.MatchString(name) {
		return "", fmt.Errorf("invalid credential reference")
	}
	value, ok := os.LookupEnv(name)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("credential reference %s is not configured", name)
	}
	return value, nil
}
