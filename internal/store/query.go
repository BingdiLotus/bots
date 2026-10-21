package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/bingdilotus/bots/internal/domain"
	"github.com/jackc/pgx/v5"
)

const (
	maxKnowledgeQueryTerms     = 8
	maxKnowledgeQueryTermRunes = 64
)

// knowledgeQueryTerms keeps recall deterministic and bounded. The model supplies
// keywords, not a full-text query: match any term, rank by how many terms each
// record contains, then prefer newer knowledge.
func knowledgeQueryTerms(query string) []string {
	terms := make([]string, 0, maxKnowledgeQueryTerms)
	seen := make(map[string]struct{}, maxKnowledgeQueryTerms)
	for _, field := range strings.Fields(query) {
		if runes := []rune(field); len(runes) > maxKnowledgeQueryTermRunes {
			field = string(runes[:maxKnowledgeQueryTermRunes])
		}
		key := strings.ToLower(field)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		terms = append(terms, field)
		if len(terms) == maxKnowledgeQueryTerms {
			break
		}
	}
	return terms
}

type reader interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

const knowledgeColumns = `id,tenant_id,bot_id,scope_id,content,kind,source_id,origin_command_id,revision,active,created_at`

func scanKnowledge(row pgx.Row) (domain.Knowledge, error) {
	var k domain.Knowledge
	err := row.Scan(&k.ID, &k.TenantID, &k.BotID, &k.ScopeID, &k.Content, &k.Kind, &k.SourceID, &k.OriginCommandID, &k.Revision, &k.Active, &k.CreatedAt)
	return k, normalized(err)
}
func queryKnowledge(ctx context.Context, q reader, scope domain.Scope, query string, n int) ([]domain.Knowledge, error) {
	if !scopeValid(scope) {
		return nil, domain.ErrInvalid
	}
	terms := knowledgeQueryTerms(query)
	if len(terms) == 0 {
		rows, err := q.Query(ctx, `SELECT `+knowledgeColumns+` FROM bots.knowledge WHERE tenant_id=$1 AND bot_id=$2 AND ($3='' OR scope_id=$3) AND active ORDER BY created_at DESC,id LIMIT $4`, scope.TenantID, scope.BotID, scope.ScopeID, limit(n))
		if err != nil {
			return nil, err
		}
		return scanKnowledgeRows(rows)
	}
	matches := make([]string, len(terms))
	scores := make([]string, len(terms))
	args := make([]any, 0, len(terms)+4)
	args = append(args, scope.TenantID, scope.BotID, scope.ScopeID)
	for i, term := range terms {
		args = append(args, term)
		// strpos treats LIKE metacharacters literally and lower() keeps matching
		// consistent across Latin keywords and mixed-case channels.
		match := fmt.Sprintf("strpos(lower(content), lower($%d)) > 0", i+4)
		matches[i], scores[i] = match, "("+match+")::int"
	}
	args = append(args, limit(n))
	statement := `SELECT ` + knowledgeColumns + ` FROM bots.knowledge WHERE tenant_id=$1 AND bot_id=$2 AND ($3='' OR scope_id=$3) AND active AND (` +
		strings.Join(matches, " OR ") + `) ORDER BY (` + strings.Join(scores, " + ") + `) DESC, created_at DESC,id LIMIT $` + fmt.Sprint(len(args))
	rows, err := q.Query(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	return scanKnowledgeRows(rows)
}

func scanKnowledgeRows(rows pgx.Rows) ([]domain.Knowledge, error) {
	defer rows.Close()
	out := []domain.Knowledge{}
	for rows.Next() {
		k, err := scanKnowledge(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}
func (s *Store) ListKnowledge(ctx context.Context, scope domain.Scope, n int) ([]domain.Knowledge, error) {
	return queryKnowledge(ctx, s.Pool, scope, "", n)
}
func (s *Store) GetTask(ctx context.Context, scope domain.Scope, id string) (domain.Task, error) {
	if !scopeValid(scope) {
		return domain.Task{}, domain.ErrInvalid
	}
	return scanTask(s.Pool.QueryRow(ctx, `SELECT `+taskColumns+` FROM bots.tasks WHERE id=$1 AND tenant_id=$2 AND bot_id=$3 AND ($4='' OR scope_id=$4)`, id, scope.TenantID, scope.BotID, scope.ScopeID))
}
func queryOpenTasks(ctx context.Context, q reader, scope domain.Scope, n int) ([]domain.Task, error) {
	if !scopeValid(scope) {
		return nil, domain.ErrInvalid
	}
	rows, err := q.Query(ctx, `SELECT `+taskColumns+` FROM bots.tasks WHERE tenant_id=$1 AND bot_id=$2 AND ($3='' OR scope_id=$3) AND status NOT IN ('completed','canceled') ORDER BY due_at ASC,id LIMIT $4`, scope.TenantID, scope.BotID, scope.ScopeID, limit(n))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Task{}
	for rows.Next() {
		item, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ListOpenTasks(ctx context.Context, scope domain.Scope, n int) ([]domain.Task, error) {
	return queryOpenTasks(ctx, s.Pool, scope, n)
}

func (s *Store) ListTasks(ctx context.Context, scope domain.Scope, n int) ([]domain.Task, error) {
	if !scopeValid(scope) {
		return nil, domain.ErrInvalid
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+taskColumns+` FROM bots.tasks WHERE tenant_id=$1 AND bot_id=$2 AND ($3='' OR scope_id=$3) ORDER BY created_at DESC,id LIMIT $4`, scope.TenantID, scope.BotID, scope.ScopeID, limit(n))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Task{}
	for rows.Next() {
		item, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
func (s *Store) ListDeliveries(ctx context.Context, scope domain.Scope, n int) ([]domain.Delivery, error) {
	if !scopeValid(scope) {
		return nil, domain.ErrInvalid
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+deliveryColumns+` FROM bots.deliveries WHERE tenant_id=$1 AND bot_id=$2 AND ($3='' OR scope_id=$3) ORDER BY created_at DESC,id LIMIT $4`, scope.TenantID, scope.BotID, scope.ScopeID, limit(n))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Delivery{}
	for rows.Next() {
		item, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
func (s *Store) ListAudit(ctx context.Context, scope domain.Scope, n int) ([]domain.AuditEvent, error) {
	if !scopeValid(scope) {
		return nil, domain.ErrInvalid
	}
	rows, err := s.Pool.Query(ctx, `SELECT id,tenant_id,bot_id,scope_id,kind,ref_id,revision,actor,data,created_at FROM bots.audit_events WHERE tenant_id=$1 AND bot_id=$2 AND ($3='' OR scope_id=$3) ORDER BY id DESC LIMIT $4`, scope.TenantID, scope.BotID, scope.ScopeID, limit(n))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.AuditEvent{}
	for rows.Next() {
		var a domain.AuditEvent
		if err := rows.Scan(&a.ID, &a.TenantID, &a.BotID, &a.ScopeID, &a.Kind, &a.RefID, &a.Revision, &a.Actor, &a.Data, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func (s *Store) DeactivateKnowledge(ctx context.Context, scope domain.Scope, id string, expected int) error {
	if !scopeValid(scope) || expected < 1 {
		return domain.ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := lockBot(ctx, tx, scope.TenantID, scope.BotID); err != nil {
		return err
	}
	k, err := scanKnowledge(tx.QueryRow(ctx, `SELECT `+knowledgeColumns+` FROM bots.knowledge WHERE id=$1 AND tenant_id=$2 AND bot_id=$3 AND ($4='' OR scope_id=$4) FOR UPDATE`, id, scope.TenantID, scope.BotID, scope.ScopeID))
	if err != nil {
		return err
	}
	if k.Revision != expected {
		return domain.ErrConflict
	}
	if _, err := tx.Exec(ctx, `UPDATE bots.knowledge SET active=false,revision=revision+1 WHERE id=$1`, id); err != nil {
		return err
	}
	if err := audit(ctx, tx, domain.Scope{TenantID: k.TenantID, BotID: k.BotID, ScopeID: k.ScopeID}, "knowledge_deactivated", id, expected+1, "admin", map[string]bool{"active": false}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) InvalidateMessage(ctx context.Context, tenant, bot, installation, channel, messageID, kind string) error {
	if kind == "edit" {
		kind = "edited"
	}
	if kind == "delete" {
		kind = "deleted"
	}
	if tenant == "" || bot == "" || installation == "" || channel == "" || messageID == "" || kind != "edited" && kind != "deleted" && kind != "permission" {
		return domain.ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	b, err := lockBot(ctx, tx, tenant, bot)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `UPDATE bots.inbox SET source_state=$6 WHERE tenant_id=$1 AND bot_id=$2 AND target->>'installation_id'=$3 AND target->>'channel_id'=$4 AND target->>'message_id'=$5 AND command_id<>'' AND source_state<>$6 AND source_state NOT IN ('deleted','permission') RETURNING command_id`, tenant, bot, installation, channel, messageID, kind)
	if err != nil {
		return err
	}
	var commands []string
	for rows.Next() {
		var command string
		if err := rows.Scan(&command); err != nil {
			rows.Close()
			return err
		}
		commands = append(commands, command)
	}
	rows.Close()
	if rows.Err() != nil {
		return rows.Err()
	}
	if len(commands) == 0 {
		return nil
	}
	tag, err := tx.Exec(ctx, `UPDATE bots.knowledge SET active=false,revision=revision+1 WHERE tenant_id=$1 AND bot_id=$2 AND origin_command_id=ANY($3) AND active`, tenant, bot, commands)
	if err != nil {
		return err
	}
	knowledgeCount := tag.RowsAffected()
	taskCount := 0
	if kind != "edited" {
		rows, err := tx.Query(ctx, `SELECT `+taskColumns+` FROM bots.tasks WHERE tenant_id=$1 AND bot_id=$2 AND origin_command_id=ANY($3) AND status NOT IN ('dispatching','unknown','completed','canceled') FOR UPDATE`, tenant, bot, commands)
		if err != nil {
			return err
		}
		var tasks []domain.Task
		for rows.Next() {
			task, err := scanTask(rows)
			if err != nil {
				rows.Close()
				return err
			}
			tasks = append(tasks, task)
		}
		rows.Close()
		if rows.Err() != nil {
			return rows.Err()
		}
		for _, task := range tasks {
			if _, err := tx.Exec(ctx, `UPDATE bots.tasks SET status='canceled',revision=revision+1 WHERE id=$1`, task.ID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE bots.command_outbox SET state='obsolete' WHERE kind='timer' AND ref_id=$1 AND state='pending'`, task.ID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE bots.deliveries SET state='obsolete',error='source invalidated' WHERE task_id=$1 AND state IN ('pending','retryable')`, task.ID); err != nil {
				return err
			}
			taskCount++
		}
	}
	if err := audit(ctx, tx, domain.Scope{TenantID: tenant, BotID: bot, ScopeID: domain.KnowledgeScope(tenant, bot, installation, channel)}, "source_"+kind, messageID, b.Revision, "channel", map[string]any{"knowledge_count": knowledgeCount, "task_count": taskCount}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
