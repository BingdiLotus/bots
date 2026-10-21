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

func TestListMemoriesAndConfirmedForget(t *testing.T) {
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

	// Another channel's knowledge is not visible or mutable from this scope.
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
	forget := func(op, id string, revision int, confirm bool) json.RawMessage {
		t.Helper()
		return f.call(t, op, "forget_memory", map[string]any{"knowledge_id": id, "expected_revision": revision, "confirm": confirm})
	}

	if got := errorCode(forget("forget-no-confirm", first, 1, false)); got != "confirmation_required" {
		t.Fatalf("missing explicit confirmation was accepted: %s", got)
	}
	if got := errorCode(forget("forget-stale", first, 99, true)); got != "conflict" {
		t.Fatalf("stale revision was accepted: %s", got)
	}
	if got := errorCode(forget("forget-cross-scope", "knowledge-other-scope", 1, true)); got != "not_found" {
		t.Fatalf("cross-scope forget was accepted: %s", got)
	}
	var active bool
	var revision int
	if err := f.s.Pool.QueryRow(t.Context(), `SELECT active,revision FROM bots.knowledge WHERE id=$1`, first).Scan(&active, &revision); err != nil {
		t.Fatal(err)
	}
	if !active || revision != 1 {
		t.Fatalf("rejected forget mutated knowledge: active=%v revision=%d", active, revision)
	}
	var otherActive bool
	if err := f.s.Pool.QueryRow(t.Context(), `SELECT active FROM bots.knowledge WHERE id='knowledge-other-scope'`).Scan(&otherActive); err != nil || !otherActive {
		t.Fatalf("cross-scope knowledge was affected: %v %v", otherActive, err)
	}

	original := forget("forget-confirmed", first, 1, true)
	var forgotten struct {
		KnowledgeID string `json:"knowledge_id"`
		Revision    int    `json:"revision"`
		Active      bool   `json:"active"`
	}
	if err := json.Unmarshal(original, &forgotten); err != nil || forgotten.KnowledgeID != first || forgotten.Revision != 2 || forgotten.Active {
		t.Fatalf("bad forget result: %s %v", original, err)
	}
	if err := f.s.Pool.QueryRow(t.Context(), `SELECT active,revision FROM bots.knowledge WHERE id=$1`, first).Scan(&active, &revision); err != nil || active || revision != 2 {
		t.Fatalf("forget did not deactivate with a new revision: active=%v revision=%d err=%v", active, revision, err)
	}
	replay := forget("forget-confirmed", first, 1, true)
	if domain.Hash(replay) != domain.Hash(original) {
		t.Fatalf("forget retry changed its result: %s / %s", original, replay)
	}
	var forgottenAudits int
	if err := f.s.Pool.QueryRow(t.Context(), `SELECT count(*) FROM bots.audit_events WHERE tenant_id=$1 AND kind='knowledge_forgotten' AND ref_id=$2`, f.tenant, first).Scan(&forgottenAudits); err != nil || forgottenAudits != 1 {
		t.Fatalf("forget audit duplicated or missing: %d %v", forgottenAudits, err)
	}
	if err := json.Unmarshal(f.call(t, "list-after-forget", "list_memories", map[string]any{}), &list); err != nil {
		t.Fatal(err)
	}
	if list.Count != 1 || list.Items[0].KnowledgeID != second {
		t.Fatalf("forgotten memory remained visible: %+v", list)
	}

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
