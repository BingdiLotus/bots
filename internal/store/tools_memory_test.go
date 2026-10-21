package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/bingdilotus/bots/internal/domain"
)

func rememberID(t *testing.T, rawResult json.RawMessage) string {
	t.Helper()
	var got struct {
		KnowledgeID string `json:"knowledge_id"`
	}
	if err := json.Unmarshal(rawResult, &got); err != nil || got.KnowledgeID == "" {
		t.Fatalf("decode knowledge id: %s %v", rawResult, err)
	}
	return got.KnowledgeID
}

func TestListMemoriesAndTwoStepForget(t *testing.T) {
	f := setupPG(t)
	first := rememberID(t, f.call(t, "remember-first", "remember", map[string]string{"content": "first durable fact", "kind": "knowledge"}))
	second := rememberID(t, f.call(t, "remember-second", "remember", map[string]string{"content": "second durable fact", "kind": "experience"}))
	if first == second {
		t.Fatal("duplicate knowledge id")
	}

	var list struct {
		Items []struct {
			KnowledgeID string    `json:"knowledge_id"`
			Content     string    `json:"content"`
			Kind        string    `json:"kind"`
			Revision    int       `json:"revision"`
			CreatedAt   time.Time `json:"created_at"`
		} `json:"items"`
		Count int `json:"count"`
	}
	if err := json.Unmarshal(f.call(t, "list-memories", "list_memories", map[string]any{}), &list); err != nil {
		t.Fatal(err)
	}
	if list.Count != 2 || len(list.Items) != 2 || list.Items[0].KnowledgeID == "" || list.Items[0].Revision != 1 {
		t.Fatalf("list_memories did not return scoped active records: %+v", list)
	}

	if _, err := f.s.Pool.Exec(t.Context(), `INSERT INTO bots.knowledge(id,tenant_id,bot_id,scope_id,content,kind,source_id,origin_command_id) VALUES('knowledge-other-scope',$1,'bot','scope-other','other channel secret','knowledge','other-source','other-command')`, f.tenant); err != nil {
		t.Fatal(err)
	}

	errorCode := func(rawResult json.RawMessage) string {
		t.Helper()
		var got struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rawResult, &got); err != nil {
			t.Fatal(err)
		}
		return got.Error.Code
	}
	assertActive := func(id string, want bool, wantRevision int) {
		t.Helper()
		var active bool
		var revision int
		if err := f.s.Pool.QueryRow(t.Context(), `SELECT active,revision FROM bots.knowledge WHERE id=$1`, id).Scan(&active, &revision); err != nil {
			t.Fatal(err)
		}
		if active != want || revision != wantRevision {
			t.Fatalf("knowledge %s active=%v revision=%d, want active=%v revision=%d", id, active, revision, want, wantRevision)
		}
	}

	if got := errorCode(f.call(t, "forget-stale", "forget_memory", map[string]any{"knowledge_id": first, "expected_revision": 99})); got != "conflict" {
		t.Fatalf("stale forget request was accepted: %s", got)
	}
	if got := errorCode(f.call(t, "forget-cross-scope", "forget_memory", map[string]any{"knowledge_id": "knowledge-other-scope", "expected_revision": 1})); got != "not_found" {
		t.Fatalf("cross-scope forget request was accepted: %s", got)
	}
	assertActive(first, true, 1)
	assertActive("knowledge-other-scope", true, 1)

	var request struct {
		Status    string    `json:"status"`
		Token     string    `json:"confirmation_token"`
		Knowledge string    `json:"knowledge_id"`
		Revision  int       `json:"expected_revision"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(f.call(t, "forget-request", "forget_memory", map[string]any{"knowledge_id": first, "expected_revision": 1}), &request); err != nil {
		t.Fatal(err)
	}
	if request.Status != "confirmation_required" || request.Token == "" || request.Knowledge != first || request.Revision != 1 || request.ExpiresAt.IsZero() {
		t.Fatalf("bad forget request: %+v", request)
	}
	assertActive(first, true, 1)

	// The model cannot satisfy confirmation in the same user request/run.
	if got := errorCode(f.call(t, "confirm-same-command", "confirm_forget_memory", map[string]any{"confirmation_token": request.Token, "knowledge_id": first, "expected_revision": 1})); got != "confirmation_invalid" {
		t.Fatalf("same-command confirm was accepted: %s", got)
	}
	if got := errorCode(f.call(t, "confirm-bad-token", "confirm_forget_memory", map[string]any{"confirmation_token": "confirm_bad", "knowledge_id": first, "expected_revision": 1})); got != "confirmation_invalid" {
		t.Fatalf("bad token was accepted: %s", got)
	}
	assertActive(first, true, 1)

	// Finish the request run and consume the token from a later user message.
	if err := f.s.FinishRun(t.Context(), f.inbox.ID, domain.RunResult{Status: "completed", Final: "confirmation requested"}); err != nil {
		t.Fatal(err)
	}
	f.inbox = f.startInput(t, "confirm-event", "confirm-thread", "confirm-run")
	confirmed := f.call(t, "confirm-after-user-message", "confirm_forget_memory", map[string]any{"confirmation_token": request.Token, "knowledge_id": first, "expected_revision": 1})
	var forgotten struct {
		KnowledgeID string `json:"knowledge_id"`
		Revision    int    `json:"revision"`
		Active      bool   `json:"active"`
	}
	if err := json.Unmarshal(confirmed, &forgotten); err != nil || forgotten.KnowledgeID != first || forgotten.Revision != 2 || forgotten.Active {
		t.Fatalf("bad confirm result: %s %v", confirmed, err)
	}
	assertActive(first, false, 2)

	var usedAt *time.Time
	if err := f.s.Pool.QueryRow(t.Context(), `SELECT used_at FROM bots.memory_confirmations WHERE token=$1`, request.Token).Scan(&usedAt); err != nil || usedAt == nil {
		t.Fatalf("confirmation token was not consumed: %v %v", usedAt, err)
	}
	replay := f.call(t, "confirm-after-user-message", "confirm_forget_memory", map[string]any{"confirmation_token": request.Token, "knowledge_id": first, "expected_revision": 1})
	if domain.Hash(replay) != domain.Hash(confirmed) {
		t.Fatalf("confirmation retry changed its result: %s / %s", confirmed, replay)
	}

	var forgottenAudits, requestedAudits int
	if err := f.s.Pool.QueryRow(t.Context(), `SELECT count(*) FROM bots.audit_events WHERE tenant_id=$1 AND kind='knowledge_forgotten' AND ref_id=$2`, f.tenant, first).Scan(&forgottenAudits); err != nil || forgottenAudits != 1 {
		t.Fatalf("forget audit duplicated or missing: %d %v", forgottenAudits, err)
	}
	if err := f.s.Pool.QueryRow(t.Context(), `SELECT count(*) FROM bots.audit_events WHERE tenant_id=$1 AND kind='memory_forget_requested' AND ref_id=$2`, f.tenant, first).Scan(&requestedAudits); err != nil || requestedAudits != 1 {
		t.Fatalf("forget request audit duplicated or missing: %d %v", requestedAudits, err)
	}
	if err := json.Unmarshal(f.call(t, "list-after-forget", "list_memories", map[string]any{}), &list); err != nil {
		t.Fatal(err)
	}
	if list.Count != 1 || list.Items[0].KnowledgeID != second {
		t.Fatalf("forgotten memory remained visible: %+v", list)
	}
	assertActive("knowledge-other-scope", true, 1)

	// Read-only listing must not consume the operation namespace or pollute the
	// mutation audit.
	var receipts, audits int
	if err := f.s.Pool.QueryRow(t.Context(), `SELECT count(*) FROM bots.inbox WHERE tenant_id=$1 AND namespace='tool' AND source_id LIKE 'list-%'`, f.tenant).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Pool.QueryRow(t.Context(), `SELECT count(*) FROM bots.audit_events WHERE tenant_id=$1 AND kind='tool_operation' AND ref_id LIKE 'list-%'`, f.tenant).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if receipts != 0 || audits != 0 {
		t.Fatalf("list_memories wrote operation state: receipts=%d audits=%d", receipts, audits)
	}
}
