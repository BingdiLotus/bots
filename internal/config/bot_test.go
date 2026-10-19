package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bingdilotus/bots/internal/domain"
)

func validConfig() domain.BotConfig {
	return domain.BotConfig{Model: "test-model", Instructions: "team assistant", ChannelKind: "slack", InstallationID: "install", WorkspaceID: "T1", BotUserID: "U1", ChannelID: "C1", TokenEnv: "TEST_TOKEN", SigningSecretEnv: "TEST_SIGNING", BridgeRef: "bridge", TimerTargetRef: "target"}
}

func TestLoadSeedRefusesRuntimeFieldsAndUnknownKeys(t *testing.T) {
	raw, err := os.ReadFile("../../configs/bot.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"runtime_agent_id: forged\n", "revision: 40\n", "unexpected: true\n", "---\nid: another\n"} {
		path := filepath.Join(t.TempDir(), "bot.yaml")
		if err := os.WriteFile(path, append(append([]byte{}, raw...), suffix...), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadSeed(path); err == nil {
			t.Fatalf("accepted configuration suffix %q", suffix)
		}
	}
	path := filepath.Join(t.TempDir(), "bot.yaml")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	bot, err := LoadSeed(path)
	if err != nil || bot.RuntimeAgentID != "" || bot.Revision != 0 || bot.Config.Timezone != "Asia/Shanghai" {
		t.Fatalf("bot=%+v error=%v", bot, err)
	}
}

func TestNormalizeBotRejectsCredentialsAndLimits(t *testing.T) {
	for _, mutate := range []func(*domain.BotConfig){
		func(c *domain.BotConfig) { c.TokenEnv = "xoxb-plaintext-secret" },
		func(c *domain.BotConfig) { c.SigningSecretEnv = "inline secret" },
		func(c *domain.BotConfig) { c.TimerTargetRef = "" },
		func(c *domain.BotConfig) { c.MaxSteps = -1 },
		func(c *domain.BotConfig) { c.MaxDurationMS = 1<<63 - 1 },
		func(c *domain.BotConfig) { c.Timezone = "invalid/zone" },
		func(c *domain.BotConfig) { c.ChannelKind = "unimplemented" },
	} {
		cfg := validConfig()
		mutate(&cfg)
		if _, err := NormalizeBot(cfg); err == nil {
			t.Fatalf("accepted %+v", cfg)
		}
	}
	cfg, err := NormalizeBot(validConfig())
	if err != nil || cfg.MaxPendingTasks != 20 || cfg.MaxDurationMS != 300000 || cfg.MaxSteps != 16 {
		t.Fatalf("defaults=%+v error=%v", cfg, err)
	}
}

func TestSecretErrorDoesNotRevealValue(t *testing.T) {
	t.Setenv("TEST_SECRET_EMPTY", "")
	if _, err := Secret("TEST_SECRET_EMPTY"); err == nil {
		t.Fatal("accepted empty secret")
	}
	if _, err := Secret("plaintext-secret"); err == nil || strings.Contains(err.Error(), "plaintext-secret") {
		t.Fatalf("unsafe error %v", err)
	}
}
