package domain

import "testing"

func TestSlackTransportDefaultsToWebhook(t *testing.T) {
	if got := (BotConfig{}).SlackTransport(); got != SlackWebhook {
		t.Fatalf("legacy deployment transport=%q, want %q", got, SlackWebhook)
	}
	if got := (BotConfig{EventTransport: SlackSocket}).SlackTransport(); got != SlackSocket {
		t.Fatalf("explicit deployment transport=%q, want %q", got, SlackSocket)
	}
}

func TestSameDeploymentFixesSlackTransportAndAppTokenReference(t *testing.T) {
	base := BotConfig{ChannelKind: "slack", InstallationID: "install", WorkspaceID: "T1", BotUserID: "U_BOT", ChannelID: "C1", TokenEnv: "BOT_TOKEN", SigningSecretEnv: "SIGNING_SECRET", BridgeRef: "bridge", TimerTargetRef: "timer"}
	explicitWebhook := base
	explicitWebhook.EventTransport = SlackWebhook
	if !base.SameDeployment(explicitWebhook) || !explicitWebhook.SameDeployment(base) {
		t.Fatal("making the legacy webhook default explicit changed the deployment")
	}
	socket := base
	socket.EventTransport = SlackSocket
	socket.AppTokenEnv = "SLACK_APP_TOKEN"
	for _, entry := range []struct {
		name   string
		mutate func(*BotConfig)
	}{
		{"transport", func(c *BotConfig) { c.EventTransport = SlackWebhook }},
		{"app token reference", func(c *BotConfig) { c.AppTokenEnv = "ANOTHER_APP_TOKEN" }},
		{"removed app token reference", func(c *BotConfig) { c.AppTokenEnv = "" }},
	} {
		t.Run(entry.name, func(t *testing.T) {
			changed := socket
			entry.mutate(&changed)
			if socket.SameDeployment(changed) || changed.SameDeployment(socket) {
				t.Fatal("Slack receiver deployment binding was mutable")
			}
		})
	}
	updated := socket
	updated.Model = "new-model"
	updated.Instructions = "updated organization instructions"
	updated.Timezone = "UTC"
	updated.MaxSteps = 8
	if !socket.SameDeployment(updated) {
		t.Fatal("runtime policy change incorrectly changed the receiver deployment")
	}
}
