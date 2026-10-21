package chronotope

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestAllNativeSchemasContainOnlyBusinessParameters(t *testing.T) {
	want := map[string][]string{
		"remember": {"content", "kind"}, "recall": {"query"}, "create_reminder": {"due_at", "text"},
		"update_reminder": {"task_id", "expected_revision", "due_at", "text"}, "cancel_reminder": {"task_id", "expected_revision"},
		"list_tasks": {}, "list_reminders": {}, "list_memories": {},
		"forget_memory":         {"knowledge_id", "expected_revision"},
		"confirm_forget_memory": {"confirmation_token", "knowledge_id", "expected_revision"},
	}
	tools := NativeTools()
	if len(tools) != 10 {
		t.Fatalf("want exactly ten native tools: %d", len(tools))
	}
	for _, tool := range tools {
		fields, exists := want[tool.ModelAlias]
		if !exists || tool.ModelAlias != tool.ToolRef || strings.TrimSpace(tool.Description) == "" {
			t.Fatalf("invalid fixed mapping: %+v", tool)
		}
		delete(want, tool.ModelAlias)
		var schema struct {
			Type                 string                    `json:"type"`
			Properties           map[string]map[string]any `json:"properties"`
			Required             []string                  `json:"required"`
			AdditionalProperties *bool                     `json:"additionalProperties"`
		}
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Fatal(err)
		}
		if schema.Type != "object" || schema.AdditionalProperties == nil || *schema.AdditionalProperties || !reflect.DeepEqual(schema.Required, fields) || len(schema.Properties) != len(fields) {
			t.Fatalf("schema is not the fixed closed business input: %s", tool.InputSchema)
		}
		for _, field := range fields {
			if _, ok := schema.Properties[field]; !ok {
				t.Fatalf("missing field %s in %s", field, tool.ModelAlias)
			}
		}
		if property, ok := schema.Properties["due_at"]; ok && (property["type"] != "string" || property["format"] != "date-time") {
			t.Fatal("due_at must carry explicit absolute-time format")
		}
		if property, ok := schema.Properties["expected_revision"]; ok && (property["type"] != "integer" || property["minimum"] != float64(1)) {
			t.Fatal("CAS revision is required and positive")
		}
		if property, ok := schema.Properties["kind"]; ok && !reflect.DeepEqual(property["enum"], []any{"knowledge", "experience"}) {
			t.Fatal("knowledge kinds do not match the native domain")
		}
		if property, ok := schema.Properties["confirmation_token"]; ok && property["type"] != "string" {
			t.Fatal("confirmation token must be an opaque string")
		}
	}
	if len(want) != 0 {
		t.Fatal("some native tools were not registered")
	}
	tools[0].InputSchema[0] = 'x'
	if !json.Valid(NativeTools()[0].InputSchema) {
		t.Fatal("callers must not mutate shared registry state")
	}
}

func TestCallbackDTOsMatchActualChronotopeWire(t *testing.T) {
	toolJSON := `{"context":{"tenant_id":"t","subject_ref":"b","scope_id":"s","origin_command_id":"o","run_id":"r","operation_id":"op","tool_call_id":"call"},"tool_ref":"remember","arguments":{"content":"x","kind":"knowledge"}}`
	var tool ApplicationToolCall
	if err := json.Unmarshal([]byte(toolJSON), &tool); err != nil {
		t.Fatal(err)
	}
	if tool.Context.TenantID != "t" || tool.Context.SubjectRef != "b" || tool.Context.ScopeID != "s" || tool.Context.OriginCommandID != "o" || tool.Context.RunID != "r" || tool.Context.OperationID != "op" || tool.Context.ToolCallID != "call" || tool.ToolRef != "remember" || !json.Valid(tool.Arguments) {
		t.Fatalf("lost trusted call context: %+v", tool)
	}
	fireJSON := `{"tenant_id":"t","timer_id":"timer","revision":2,"fire_id":"fire","opaque_action_ref":"task"}`
	var fire TimerFire
	if err := json.Unmarshal([]byte(fireJSON), &fire); err != nil {
		t.Fatal(err)
	}
	if fire.TenantID != "t" || fire.TimerID != "timer" || fire.Revision != 2 || fire.FireID != "fire" || fire.OpaqueActionRef != "task" {
		t.Fatalf("wrong timer fire shape: %+v", fire)
	}
	raw, err := json.Marshal(fire)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 5 {
		t.Fatalf("do not invent unavailable subject/scope fields: %s", raw)
	}
}
