// Package chronotope implements the application HTTP contract of the independent
// Chronotope runtime. These wire types deliberately do not import its internals
// or own Bots business state, model history, dispatch, or retry policy.
package chronotope

import (
	"encoding/json"
	"time"
)

type ApplicationTool struct {
	ModelAlias  string          `json:"model_alias"`
	ToolRef     string          `json:"tool_ref"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type AgentConfig struct {
	Model                string            `json:"model"`
	Instructions         string            `json:"instructions"`
	Tools                []string          `json:"tools"`
	Version              int               `json:"version"`
	ApplicationProfile   bool              `json:"application_profile"`
	ApplicationBridgeRef string            `json:"application_bridge_ref"`
	ApplicationTools     []ApplicationTool `json:"application_tools"`
	MaxOutputTokens      int               `json:"max_output_tokens,omitempty"`
	MaxSteps             int               `json:"max_steps,omitempty"`
	MaxDurationMS        int64             `json:"max_duration_ms,omitempty"`
	OutputSchema         json.RawMessage   `json:"output_schema,omitempty"`
}

type CreateAgentRequest struct {
	Name   string      `json:"name"`
	Config AgentConfig `json:"config"`
}

type Agent struct {
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	Config     AgentConfig `json:"config"`
	SpecDigest string      `json:"spec_digest"`
	Idempotent bool        `json:"idempotent"`
}

type CreateSessionRequest struct {
	SubjectRef string `json:"subject_ref"`
	ScopeID    string `json:"scope_id"`
}

type Session struct {
	ID         string `json:"id"`
	AgentID    string `json:"agent_id"`
	Status     string `json:"status"`
	SubjectRef string `json:"subject_ref"`
	ScopeID    string `json:"scope_id"`
	Idempotent bool   `json:"idempotent"`
}

type SubmitRunRequest struct {
	Input           string         `json:"input"`
	Topic           string         `json:"topic,omitempty"`
	Trigger         map[string]any `json:"trigger,omitempty"`
	OriginCommandID string         `json:"origin_command_id,omitempty"`
}

type RunStatus string

const (
	RunQueued           RunStatus = "queued"
	RunRunning          RunStatus = "running"
	RunPaused           RunStatus = "paused"
	RunAwaitingApproval RunStatus = "awaiting_approval"
	RunFrozen           RunStatus = "frozen"
	RunCompleted        RunStatus = "completed"
	RunFailed           RunStatus = "failed"
	RunCanceled         RunStatus = "canceled"
)

func (s RunStatus) Terminal() bool {
	return s == RunCompleted || s == RunFailed || s == RunCanceled
}

func (s RunStatus) valid() bool {
	switch s {
	case RunQueued, RunRunning, RunPaused, RunAwaitingApproval, RunFrozen, RunCompleted, RunFailed, RunCanceled:
		return true
	}
	return false
}

type RunHandle struct {
	RunID      string    `json:"run_id"`
	Status     RunStatus `json:"status"`
	Idempotent bool      `json:"idempotent"`
}

type RunError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type RunUsage struct {
	TokensIn       int64   `json:"tokens_in"`
	TokensOut      int64   `json:"tokens_out"`
	TokensTotal    int64   `json:"tokens_total"`
	ComputeSeconds float64 `json:"compute_seconds"`
	UsageUnknown   bool    `json:"usage_unknown"`
}

type RunBinding struct {
	AgentConfigVersion int         `json:"agent_config_version"`
	AgentConfig        AgentConfig `json:"agent_config"`
	SpecDigest         string      `json:"spec_digest"`
	ProtocolVersion    string      `json:"protocol_version"`
	Model              string      `json:"model"`
	TenantID           string      `json:"tenant_id"`
	SubjectRef         string      `json:"subject_ref"`
	ScopeID            string      `json:"scope_id"`
	OriginCommandID    string      `json:"origin_command_id"`
	DeadlineAt         time.Time   `json:"deadline_at"`
}

type Run struct {
	RunID     string     `json:"run_id"`
	SessionID string     `json:"session_id"`
	Status    RunStatus  `json:"status"`
	Final     *string    `json:"final"`
	Error     *RunError  `json:"error"`
	Usage     *RunUsage  `json:"usage"`
	Bound     RunBinding `json:"bound"`
	Steps     int        `json:"steps,omitempty"`
	Truncated bool       `json:"truncated,omitempty"`
}

type TimerRequest struct {
	TenantID        string    `json:"tenant_id"`
	SubjectRef      string    `json:"subject_ref"`
	ScopeID         string    `json:"scope_id"`
	TimerID         string    `json:"timer_id"`
	Revision        int64     `json:"revision"`
	DueAt           time.Time `json:"due_at"`
	OpaqueActionRef string    `json:"opaque_action_ref"`
}

type TimerQuery struct {
	TenantID   string
	SubjectRef string
	ScopeID    string
	TimerID    string
	Revision   int64
}

type Timer struct {
	TimerRequest
	TimerHandle      string     `json:"timer_handle"`
	FireID           string     `json:"fire_id"`
	RequestDigest    string     `json:"request_digest"`
	TargetRef        string     `json:"target_ref"`
	TargetDigest     string     `json:"target_digest"`
	State            string     `json:"state"`
	SubmitAttempts   int        `json:"submit_attempts"`
	CallbackAttempts int        `json:"callback_attempts"`
	LastError        string     `json:"last_error,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	SubmittedAt      *time.Time `json:"submitted_at,omitempty"`
	AcknowledgedAt   *time.Time `json:"acknowledged_at,omitempty"`
}

// ToolContext is injected by Chronotope from its bound run, not model arguments.
// The receiving application must authenticate the service and verify this context.
type ToolContext struct {
	TenantID        string `json:"tenant_id"`
	SubjectRef      string `json:"subject_ref"`
	ScopeID         string `json:"scope_id"`
	OriginCommandID string `json:"origin_command_id"`
	RunID           string `json:"run_id"`
	OperationID     string `json:"operation_id"`
	ToolCallID      string `json:"tool_call_id"`
}

type ApplicationToolCall struct {
	Context   ToolContext     `json:"context"`
	ToolRef   string          `json:"tool_ref"`
	Arguments json.RawMessage `json:"arguments"`
}

// TimerFire intentionally has no subject/scope fields: the receiver resolves
// them using its durable timer/task association and authenticated fixed route.
type TimerFire struct {
	TenantID        string `json:"tenant_id"`
	TimerID         string `json:"timer_id"`
	Revision        int64  `json:"revision"`
	FireID          string `json:"fire_id"`
	OpaqueActionRef string `json:"opaque_action_ref"`
}
