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

func TestNormalizeBotSlackTransportModes(t *testing.T) {
	for _, entry := range []struct {
		name      string
		mutate    func(*domain.BotConfig)
		transport string
		valid     bool
	}{
		{name: "legacy defaults to webhook", transport: domain.SlackWebhook, valid: true},
		{name: "explicit webhook", mutate: func(c *domain.BotConfig) { c.EventTransport = domain.SlackWebhook }, transport: domain.SlackWebhook, valid: true},
		{name: "default webhook requires signing secret", mutate: func(c *domain.BotConfig) { c.SigningSecretEnv = "" }},
		{name: "explicit webhook requires signing secret", mutate: func(c *domain.BotConfig) {
			c.EventTransport = domain.SlackWebhook
			c.SigningSecretEnv = ""
		}},
		{name: "socket needs no signing secret", mutate: func(c *domain.BotConfig) {
			c.EventTransport = domain.SlackSocket
			c.AppTokenEnv = "TEST_SLACK_APP_TOKEN"
			c.SigningSecretEnv = ""
		}, transport: domain.SlackSocket, valid: true},
		{name: "socket requires app token reference", mutate: func(c *domain.BotConfig) { c.EventTransport = domain.SlackSocket }},
		{name: "socket requires workspace", mutate: func(c *domain.BotConfig) {
			c.EventTransport = domain.SlackSocket
			c.AppTokenEnv = "TEST_SLACK_APP_TOKEN"
			c.WorkspaceID = ""
		}},
		{name: "webhook requires workspace", mutate: func(c *domain.BotConfig) { c.WorkspaceID = "" }},
		{name: "unsupported transport", mutate: func(c *domain.BotConfig) { c.EventTransport = "poll" }},
		{name: "socket app token must be env reference", mutate: func(c *domain.BotConfig) {
			c.EventTransport = domain.SlackSocket
			c.AppTokenEnv = "xapp-plaintext-secret"
		}},
		{name: "webhook rejects invalid optional app token reference", mutate: func(c *domain.BotConfig) { c.AppTokenEnv = "inline app token" }},
		{name: "socket rejects invalid optional signing reference", mutate: func(c *domain.BotConfig) {
			c.EventTransport = domain.SlackSocket
			c.AppTokenEnv = "TEST_SLACK_APP_TOKEN"
			c.SigningSecretEnv = "inline signing secret"
		}},
		{name: "socket bot token must be env reference", mutate: func(c *domain.BotConfig) {
			c.EventTransport = domain.SlackSocket
			c.AppTokenEnv = "TEST_SLACK_APP_TOKEN"
			c.TokenEnv = "xoxb-plaintext-secret"
		}},
	} {
		t.Run(entry.name, func(t *testing.T) {
			cfg := validConfig()
			if entry.mutate != nil {
				entry.mutate(&cfg)
			}
			got, err := NormalizeBot(cfg)
			if entry.valid {
				if err != nil || got.SlackTransport() != entry.transport || got.AppTokenEnv != cfg.AppTokenEnv || got.SigningSecretEnv != cfg.SigningSecretEnv {
					t.Fatalf("configuration changed transport or credential references: %+v error=%v", got, err)
				}
			} else if err == nil {
				t.Fatal("invalid Slack transport configuration was accepted")
			}
		})
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
