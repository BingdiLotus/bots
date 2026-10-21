package domain

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrNotFound            = errors.New("not found")
	ErrConflict            = errors.New("request or revision conflict")
	ErrDisabled            = errors.New("bot or account disabled")
	ErrBusy                = errors.New("operation already in flight or result unknown")
	ErrLimit               = errors.New("pending task limit reached")
	ErrInvalid             = errors.New("invalid request")
	ErrConfirmationInvalid = errors.New("confirmation token invalid, expired, used, or from the same request")
)

type Scope struct {
	TenantID string `json:"tenant_id"`
	BotID    string `json:"bot_id"`
	ScopeID  string `json:"scope_id"`
}

type Target struct {
	Provider       string `json:"provider"`
	InstallationID string `json:"installation_id"`
	ChannelID      string `json:"channel_id"`
	ThreadID       string `json:"thread_id"`
	MessageID      string `json:"message_id,omitempty"`
}

type BotConfig struct {
	Model            string `json:"model"`
	Instructions     string `json:"instructions"`
	Timezone         string `json:"timezone"`
	ChannelKind      string `json:"channel_kind"`
	InstallationID   string `json:"installation_id"`
	WorkspaceID      string `json:"workspace_id,omitempty"`
	BotUserID        string `json:"bot_user_id"`
	ChannelID        string `json:"channel_id,omitempty"`
	TokenEnv         string `json:"token_env"`
	SigningSecretEnv string `json:"signing_secret_env,omitempty"`
	EventTransport   string `json:"event_transport,omitempty"`
	AppTokenEnv      string `json:"app_token_env,omitempty"`
	BridgeRef        string `json:"bridge_ref"`
	TimerTargetRef   string `json:"timer_target_ref"`
	MaxOutputTokens  int    `json:"max_output_tokens"`
	MaxSteps         int    `json:"max_steps"`
	MaxDurationMS    int64  `json:"max_duration_ms"`
	MaxPendingTasks  int    `json:"max_pending_tasks"`
}

const (
	SlackWebhook = "webhook"
	SlackSocket  = "socket"
)

// SlackTransport defaults to the signed webhook ingress when unspecified.
func (c BotConfig) SlackTransport() string {
	if c.EventTransport == "" {
		return SlackWebhook
	}
	return c.EventTransport
}

// SameDeployment fixes the account and endpoint bindings for a Bot's lifetime.
// Credential values can rotate at the original environment reference.
func (c BotConfig) SameDeployment(other BotConfig) bool {
	return c.ChannelKind == other.ChannelKind &&
		c.InstallationID == other.InstallationID &&
		c.WorkspaceID == other.WorkspaceID &&
		c.BotUserID == other.BotUserID &&
		c.ChannelID == other.ChannelID &&
		c.TokenEnv == other.TokenEnv &&
		c.SigningSecretEnv == other.SigningSecretEnv &&
		c.SlackTransport() == other.SlackTransport() &&
		c.AppTokenEnv == other.AppTokenEnv &&
		c.BridgeRef == other.BridgeRef &&
		c.TimerTargetRef == other.TimerTargetRef
}

type Bot struct {
	TenantID       string    `json:"tenant_id"`
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Enabled        bool      `json:"enabled"`
	AccountEnabled bool      `json:"account_enabled"`
	Revision       int       `json:"revision"`
	ConfigHash     string    `json:"config_hash"`
	RuntimeAgentID string    `json:"runtime_agent_id"`
	Config         BotConfig `json:"config"`
	CreatedAt      time.Time `json:"created_at"`
}

type RuntimeSession struct {
	Key              string `json:"key"`
	TenantID         string `json:"tenant_id"`
	BotID            string `json:"bot_id"`
	ScopeID          string `json:"scope_id"`
	Revision         int    `json:"revision"`
	RuntimeAgentID   string `json:"runtime_agent_id"`
	RuntimeSessionID string `json:"runtime_session_id"`
	Target           Target `json:"target"`
}

type Inbox struct {
	ID               string    `json:"id"`
	TenantID         string    `json:"tenant_id"`
	BotID            string    `json:"bot_id"`
	ScopeID          string    `json:"scope_id"`
	Namespace        string    `json:"namespace"`
	SourceID         string    `json:"source_id"`
	CommandID        string    `json:"command_id"`
	SessionKey       string    `json:"session_key"`
	Input            string    `json:"input"`
	Target           Target    `json:"target"`
	Revision         int       `json:"revision"`
	RuntimeSessionID string    `json:"runtime_session_id"`
	RunID            string    `json:"run_id"`
	State            string    `json:"state"`
	SourceState      string    `json:"source_state"`
	Error            string    `json:"error,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

type Knowledge struct {
	ID              string    `json:"id"`
	TenantID        string    `json:"tenant_id"`
	BotID           string    `json:"bot_id"`
	ScopeID         string    `json:"scope_id"`
	Content         string    `json:"content"`
	Kind            string    `json:"kind"`
	SourceID        string    `json:"source_id"`
	OriginCommandID string    `json:"origin_command_id"`
	Revision        int       `json:"revision"`
	Active          bool      `json:"active"`
	CreatedAt       time.Time `json:"created_at"`
}

type Task struct {
	ID              string    `json:"id"`
	TenantID        string    `json:"tenant_id"`
	BotID           string    `json:"bot_id"`
	ScopeID         string    `json:"scope_id"`
	OriginCommandID string    `json:"origin_command_id"`
	ReminderSlot    int       `json:"reminder_slot"`
	Text            string    `json:"text"`
	DueAt           time.Time `json:"due_at"`
	Status          string    `json:"status"`
	Revision        int       `json:"revision"`
	TimerID         string    `json:"timer_id"`
	TimerHandle     string    `json:"timer_handle"`
	FireID          string    `json:"fire_id"`
	DeliveryID      string    `json:"delivery_id"`
	Target          Target    `json:"target"`
	CreatedAt       time.Time `json:"created_at"`
}

type Command struct {
	ID            string          `json:"id"`
	TenantID      string          `json:"tenant_id"`
	BotID         string          `json:"bot_id"`
	Kind          string          `json:"kind"`
	RefID         string          `json:"ref_id"`
	Revision      int             `json:"revision"`
	RequestHash   string          `json:"request_hash"`
	Payload       json.RawMessage `json:"payload"`
	Handle        string          `json:"handle"`
	State         string          `json:"state"`
	Error         string          `json:"error,omitempty"`
	Attempts      int             `json:"attempts"`
	NextAttemptAt time.Time       `json:"next_attempt_at"`
}

type Delivery struct {
	ID                string    `json:"id"`
	OperationID       string    `json:"operation_id"`
	TenantID          string    `json:"tenant_id"`
	BotID             string    `json:"bot_id"`
	ScopeID           string    `json:"scope_id"`
	TaskID            string    `json:"task_id,omitempty"`
	TaskRevision      int       `json:"task_revision,omitempty"`
	CommandID         string    `json:"command_id,omitempty"`
	Target            Target    `json:"target"`
	Text              string    `json:"text"`
	PayloadHash       string    `json:"payload_hash"`
	State             string    `json:"state"`
	ProviderMessageID string    `json:"provider_message_id,omitempty"`
	Error             string    `json:"error,omitempty"`
	Attempts          int       `json:"attempts"`
	NextAttemptAt     time.Time `json:"next_attempt_at"`
	CreatedAt         time.Time `json:"created_at"`
}

type AuditEvent struct {
	ID        int64           `json:"id"`
	TenantID  string          `json:"tenant_id"`
	BotID     string          `json:"bot_id"`
	ScopeID   string          `json:"scope_id,omitempty"`
	Kind      string          `json:"kind"`
	RefID     string          `json:"ref_id"`
	Revision  int             `json:"revision"`
	Actor     string          `json:"actor"`
	Data      json.RawMessage `json:"data"`
	CreatedAt time.Time       `json:"created_at"`
}

// Metrics is the small, non-secret trial snapshot exposed on the loopback
// metrics endpoint. State maps are current gauges, not billing counters.
type Metrics struct {
	Inbox           map[string]int `json:"inbox"`
	Tasks           map[string]int `json:"tasks"`
	Deliveries      map[string]int `json:"deliveries"`
	KnowledgeActive int            `json:"knowledge_active"`
}

type ToolContext struct {
	TenantID        string `json:"tenant_id"`
	SubjectRef      string `json:"subject_ref"`
	ScopeID         string `json:"scope_id"`
	OriginCommandID string `json:"origin_command_id"`
	RunID           string `json:"run_id"`
	OperationID     string `json:"operation_id"`
	ToolCallID      string `json:"tool_call_id"`
}
type ToolCall struct {
	Context   ToolContext     `json:"context"`
	ToolRef   string          `json:"tool_ref"`
	Arguments json.RawMessage `json:"arguments"`
}
type TimerFire struct {
	TenantID        string `json:"tenant_id"`
	TimerID         string `json:"timer_id"`
	Revision        int    `json:"revision"`
	FireID          string `json:"fire_id"`
	OpaqueActionRef string `json:"opaque_action_ref"`
}
type RunResult struct {
	Status string          `json:"status"`
	Final  string          `json:"final"`
	Error  json.RawMessage `json:"error"`
	Usage  json.RawMessage `json:"usage"`
}

func StableID(prefix string, parts ...string) string {
	raw, _ := json.Marshal(parts)
	sum := sha256.Sum256(raw)
	return prefix + hex.EncodeToString(sum[:16])
}
func NewID(prefix string) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(value[:]), nil
}
func Hash(value any) string {
	raw, _ := json.Marshal(value)
	var canonical any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&canonical) == nil {
		raw, _ = json.Marshal(canonical)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func KnowledgeScope(tenant, bot, installation, channel string) string {
	return StableID("scope_", tenant, bot, installation, channel)
}
func SessionKey(tenant, bot string, target Target) string {
	return StableID("session_", tenant, bot, target.Provider, target.InstallationID, target.ChannelID, target.ThreadID)
}
