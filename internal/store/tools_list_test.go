package store

import (
	"encoding/json"
	"testing"
	"time"
)

type listedTask struct {
	TaskID   string    `json:"task_id"`
	Status   string    `json:"status"`
	DueAt    time.Time `json:"due_at"`
	Text     string    `json:"text"`
	Revision int       `json:"revision"`
}

func listResult(t *testing.T, rawResult json.RawMessage) ([]listedTask, int) {
	t.Helper()
	var got struct {
		Items []listedTask `json:"items"`
		Count int          `json:"count"`
	}
	if err := json.Unmarshal(rawResult, &got); err != nil {
		t.Fatalf("decode list result: %v (%s)", err, rawResult)
	}
	if got.Items == nil {
		t.Fatalf("items must be an array, not null: %s", rawResult)
	}
	return got.Items, got.Count
}

func TestListTasksIsScopedReadOnlyAndExcludesClosed(t *testing.T) {
	f := setupPG(t)
	first := f.createTask(t)

	// A task in a different channel scope must not leak into this channel's
	// projection. Insert it directly because the single-active-inbox invariant
	// intentionally prevents a second in-flight command for this test bot.
	if _, err := f.s.Pool.Exec(t.Context(), `INSERT INTO bots.tasks(id,tenant_id,bot_id,scope_id,origin_command_id,text,due_at,status,timer_id,target) VALUES('task-other-scope',$1,'bot','scope-other','command-other','other channel task',now()+interval '2 minutes','pending_schedule','timer-other','{}')`, f.tenant); err != nil {
		t.Fatal(err)
	}

	items, count := listResult(t, f.call(t, "list-one", "list_tasks", map[string]any{}))
	if count != 1 || len(items) != 1 || items[0].TaskID != first.ID || items[0].Text != first.Text || items[0].Status != "pending_schedule" {
		t.Fatalf("list_tasks returned wrong scope/open set: %+v count=%d", items, count)
	}
	reminders, reminderCount := listResult(t, f.call(t, "list-reminders", "list_reminders", map[string]any{}))
	if reminderCount != 1 || len(reminders) != 1 || reminders[0].TaskID != first.ID {
		t.Fatalf("list_reminders disagrees with list_tasks: %+v count=%d", reminders, reminderCount)
	}

	// Read-only projections deliberately do not create operation receipts or
	// mutation audit; otherwise a status question consumes the durable outbox
	// namespace and pollutes the audit stream.
	var receipts, audits int
	if err := f.s.Pool.QueryRow(t.Context(), `SELECT count(*) FROM bots.inbox WHERE tenant_id=$1 AND namespace='tool' AND source_id LIKE 'list-%'`, f.tenant).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Pool.QueryRow(t.Context(), `SELECT count(*) FROM bots.audit_events WHERE tenant_id=$1 AND kind='tool_operation' AND ref_id LIKE 'list-%'`, f.tenant).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if receipts != 0 || audits != 0 {
		t.Fatalf("read-only list wrote operation state: receipts=%d audits=%d", receipts, audits)
	}

	// unknown/blocked are unresolved responsibilities and must remain visible.
	if _, err := f.s.Pool.Exec(t.Context(), `UPDATE bots.tasks SET status='unknown' WHERE id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	items, count = listResult(t, f.call(t, "list-unknown", "list_tasks", map[string]any{}))
	if count != 1 || len(items) != 1 || items[0].Status != "unknown" {
		t.Fatalf("unresolved task was hidden: %+v count=%d", items, count)
	}
	if _, err := f.s.Pool.Exec(t.Context(), `UPDATE bots.tasks SET status='completed' WHERE id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	items, count = listResult(t, f.call(t, "list-after-close", "list_tasks", map[string]any{}))
	if count != 0 || len(items) != 0 {
		t.Fatalf("completed task remained in list: %+v count=%d", items, count)
	}
}
