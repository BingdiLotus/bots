package store

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/bingdilotus/bots/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schema string

// Derive the readiness check from the DDL so new tables and indexes cannot be
// omitted from a separate list. Bootstrap still executes the complete DDL when
// any relation is missing.
var schemaTables = schemaNames(`(?m)^CREATE TABLE IF NOT EXISTS bots\.([a-z_]+)`)
var schemaIndexes = schemaNames(`(?m)^CREATE (?:UNIQUE )?INDEX IF NOT EXISTS ([a-z_]+)`)

func schemaNames(pattern string) []string {
	matches := regexp.MustCompile(pattern).FindAllStringSubmatch(schema, -1)
	names := make([]string, len(matches))
	for i, match := range matches {
		names[i] = match[1]
	}
	return names
}

type Store struct{ Pool *pgxpool.Pool }

func Open(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 8
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "10000"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{Pool: pool}, nil
}
func (s *Store) Close() { s.Pool.Close() }
func (s *Store) Bootstrap(ctx context.Context) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := bootstrapSchema(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func bootstrapSchema(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('bingdi-bots:schema',0))`); err != nil {
		return err
	}
	ready, err := schemaReady(ctx, tx)
	if err != nil || ready {
		return err
	}
	// Even CREATE INDEX IF NOT EXISTS takes a table ShareLock before checking
	// whether its index exists. Repeating it on a ready database can deadlock
	// with transactions that write these tables in a different order.
	if _, err := tx.Exec(ctx, schema); err != nil {
		return err
	}
	ready, err = schemaReady(ctx, tx)
	if err != nil {
		return err
	}
	if !ready {
		return errors.New("bots schema has missing or invalid relations after bootstrap")
	}
	return nil
}

func schemaReady(ctx context.Context, tx pgx.Tx) (bool, error) {
	if len(schemaTables) == 0 || len(schemaIndexes) == 0 {
		return false, errors.New("bots schema readiness check found no table or index declarations")
	}
	var ready bool
	err := tx.QueryRow(ctx, `
		WITH expected AS (
			SELECT name, 'r'::"char" AS kind FROM unnest($1::text[]) AS name
			UNION ALL
			SELECT name, 'i'::"char" AS kind FROM unnest($2::text[]) AS name
		)
		SELECT NOT EXISTS (
			SELECT FROM expected e
			LEFT JOIN pg_catalog.pg_namespace n ON n.nspname='bots'
			LEFT JOIN pg_catalog.pg_class c ON c.relnamespace=n.oid AND c.relname=e.name AND c.relkind=e.kind
			LEFT JOIN pg_catalog.pg_index i ON i.indexrelid=c.oid
			WHERE c.oid IS NULL OR (e.kind='i' AND (NOT i.indisvalid OR NOT i.indisready))
		)`, schemaTables, schemaIndexes).Scan(&ready)
	return ready, err
}

func normalized(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	return err
}
func raw(v any) []byte { b, _ := json.Marshal(v); return b }
func limit(n int) int {
	if n <= 0 || n > 200 {
		return 50
	}
	return n
}
func scopeValid(scope domain.Scope) bool { return scope.TenantID != "" && scope.BotID != "" }

type queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

const botColumns = `tenant_id,bot_id,name,enabled,account_enabled,revision,config_hash,runtime_agent_id,config,created_at`

func scanBot(row pgx.Row) (domain.Bot, error) {
	var b domain.Bot
	var config []byte
	err := row.Scan(&b.TenantID, &b.ID, &b.Name, &b.Enabled, &b.AccountEnabled, &b.Revision, &b.ConfigHash, &b.RuntimeAgentID, &config, &b.CreatedAt)
	if err != nil {
		return b, normalized(err)
	}
	err = json.Unmarshal(config, &b.Config)
	return b, err
}
func lockBot(ctx context.Context, tx pgx.Tx, tenant, bot string) (domain.Bot, error) {
	return scanBot(tx.QueryRow(ctx, `SELECT `+botColumns+` FROM bots.bots WHERE tenant_id=$1 AND bot_id=$2 FOR UPDATE`, tenant, bot))
}
func audit(ctx context.Context, tx pgx.Tx, scope domain.Scope, kind, ref string, revision int, actor string, data any) error {
	_, err := tx.Exec(ctx, `INSERT INTO bots.audit_events(tenant_id,bot_id,scope_id,kind,ref_id,revision,actor,data) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, scope.TenantID, scope.BotID, scope.ScopeID, kind, ref, revision, actor, raw(data))
	return err
}
func defaults(c domain.BotConfig) domain.BotConfig {
	if c.Timezone == "" {
		c.Timezone = "Asia/Shanghai"
	}
	if c.MaxPendingTasks <= 0 {
		c.MaxPendingTasks = 20
	}
	if c.MaxSteps <= 0 {
		c.MaxSteps = 16
	}
	if c.MaxOutputTokens <= 0 {
		c.MaxOutputTokens = 4096
	}
	if c.MaxDurationMS <= 0 {
		c.MaxDurationMS = 300000
	}
	return c
}
func validateConfig(c domain.BotConfig) error {
	if c.Timezone != "" {
		if _, err := time.LoadLocation(c.Timezone); err != nil {
			return domain.ErrInvalid
		}
	}
	if c.MaxDurationMS > int64((1<<63-1)/time.Millisecond) {
		return domain.ErrInvalid
	}
	if c.Model == "" || c.ChannelKind == "" || c.InstallationID == "" || c.TokenEnv == "" || c.BridgeRef == "" || c.TimerTargetRef == "" || c.MaxPendingTasks < 0 || c.MaxSteps < 0 || c.MaxOutputTokens < 0 || c.MaxDurationMS < 0 {
		return domain.ErrInvalid
	}
	return nil
}

func (s *Store) ImportBot(ctx context.Context, b domain.Bot) (domain.Bot, error) {
	if b.TenantID == "" || b.ID == "" {
		return b, domain.ErrInvalid
	}
	if err := validateConfig(b.Config); err != nil {
		return b, err
	}
	b.Config = defaults(b.Config)
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return b, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `INSERT INTO bots.bots(tenant_id,bot_id,name,enabled,account_enabled,config_hash,config) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, b.TenantID, b.ID, b.Name, b.Enabled, b.AccountEnabled, domain.Hash(b.Config), raw(b.Config))
	if err != nil {
		return b, err
	}
	if tag.RowsAffected() == 1 {
		if err := audit(ctx, tx, domain.Scope{TenantID: b.TenantID, BotID: b.ID}, "config_revision", b.ID, 1, "import", map[string]any{"config": b.Config, "hash": domain.Hash(b.Config), "runtime_agent_id": ""}); err != nil {
			return b, err
		}
		if err := putCommand(ctx, tx, domain.Command{ID: domain.StableID("agent_", b.TenantID, b.ID, "1"), TenantID: b.TenantID, BotID: b.ID, Kind: "agent", RefID: b.ID, Revision: 1, Payload: raw(b.Config)}); err != nil {
			return b, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return b, err
	}
	return s.GetBot(ctx, b.TenantID, b.ID)
}
func (s *Store) GetBot(ctx context.Context, tenant, bot string) (domain.Bot, error) {
	return scanBot(s.Pool.QueryRow(ctx, `SELECT `+botColumns+` FROM bots.bots WHERE tenant_id=$1 AND bot_id=$2`, tenant, bot))
}
func (s *Store) ListBots(ctx context.Context, tenant string) ([]domain.Bot, error) {
	if tenant == "" {
		return nil, domain.ErrInvalid
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+botColumns+` FROM bots.bots WHERE tenant_id=$1 ORDER BY bot_id`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Bot{}
	for rows.Next() {
		b, err := scanBot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
func (s *Store) SetBotEnabled(ctx context.Context, tenant, bot string, enabled bool) error {
	return s.setEnabled(ctx, tenant, bot, enabled, false)
}
func (s *Store) SetAccountEnabled(ctx context.Context, tenant, bot string, enabled bool) error {
	return s.setEnabled(ctx, tenant, bot, enabled, true)
}
func (s *Store) setEnabled(ctx context.Context, tenant, bot string, enabled, account bool) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	b, err := lockBot(ctx, tx, tenant, bot)
	if err != nil {
		return err
	}
	column, kind := "enabled", "bot_enabled"
	if account {
		column, kind = "account_enabled", "account_enabled"
	}
	if _, err := tx.Exec(ctx, `UPDATE bots.bots SET `+column+`=$3 WHERE tenant_id=$1 AND bot_id=$2`, tenant, bot, enabled); err != nil {
		return err
	}
	if err := audit(ctx, tx, domain.Scope{TenantID: tenant, BotID: bot}, kind, bot, b.Revision, "admin", map[string]bool{"enabled": enabled}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) SetRuntimeAgent(ctx context.Context, tenant, bot string, revision int, id string) error {
	if id == "" {
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
	if b.Revision != revision || b.RuntimeAgentID != "" && b.RuntimeAgentID != id {
		return domain.ErrConflict
	}
	if _, err := tx.Exec(ctx, `UPDATE bots.bots SET runtime_agent_id=$3 WHERE tenant_id=$1 AND bot_id=$2`, tenant, bot, id); err != nil {
		return err
	}
	if b.RuntimeAgentID == "" {
		if err := audit(ctx, tx, domain.Scope{TenantID: tenant, BotID: bot}, "runtime_agent_bound", id, revision, "runtime", map[string]string{"runtime_agent_id": id}); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE bots.command_outbox SET state='accepted',handle=$4 WHERE tenant_id=$1 AND bot_id=$2 AND kind='agent' AND revision=$3`, tenant, bot, revision, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) ConfigRevision(ctx context.Context, tenant, bot string, revision int) (domain.BotConfig, string, error) {
	var data []byte
	err := s.Pool.QueryRow(ctx, `SELECT data FROM bots.audit_events WHERE tenant_id=$1 AND bot_id=$2 AND revision=$3 AND kind='config_revision'`, tenant, bot, revision).Scan(&data)
	if err != nil {
		return domain.BotConfig{}, "", normalized(err)
	}
	var result struct {
		Config domain.BotConfig `json:"config"`
		Agent  string           `json:"runtime_agent_id"`
	}
	err = json.Unmarshal(data, &result)
	if err != nil {
		return result.Config, "", err
	}
	if result.Agent == "" {
		var agent string
		bindErr := s.Pool.QueryRow(ctx, `SELECT data->>'runtime_agent_id' FROM bots.audit_events WHERE tenant_id=$1 AND bot_id=$2 AND revision=$3 AND kind='runtime_agent_bound' ORDER BY id LIMIT 1`, tenant, bot, revision).Scan(&agent)
		if bindErr == nil {
			result.Agent = agent
		} else if !errors.Is(bindErr, pgx.ErrNoRows) {
			return result.Config, "", bindErr
		}
	}
	return result.Config, result.Agent, nil
}
func (s *Store) UpdateConfig(ctx context.Context, tenant, bot string, expected int, cfg domain.BotConfig, agent string) (domain.Bot, error) {
	if agent == "" {
		return domain.Bot{}, domain.ErrInvalid
	}
	if err := validateConfig(cfg); err != nil {
		return domain.Bot{}, err
	}
	cfg = defaults(cfg)
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return domain.Bot{}, err
	}
	defer tx.Rollback(ctx)
	b, err := lockBot(ctx, tx, tenant, bot)
	if err != nil {
		return b, err
	}
	if b.Revision != expected {
		return b, domain.ErrConflict
	}
	if !b.Config.SameDeployment(cfg) {
		return b, domain.ErrInvalid
	}
	if _, err := tx.Exec(ctx, `UPDATE bots.bots SET config=$3,config_hash=$4,revision=revision+1,runtime_agent_id=$5 WHERE tenant_id=$1 AND bot_id=$2`, tenant, bot, raw(cfg), domain.Hash(cfg), agent); err != nil {
		return b, err
	}
	if err := audit(ctx, tx, domain.Scope{TenantID: tenant, BotID: bot}, "config_revision", bot, expected+1, "admin", map[string]any{"config": cfg, "hash": domain.Hash(cfg), "runtime_agent_id": agent}); err != nil {
		return b, err
	}
	if err := tx.Commit(ctx); err != nil {
		return b, err
	}
	return s.GetBot(ctx, tenant, bot)
}

func putCommand(ctx context.Context, tx pgx.Tx, c domain.Command) error {
	if c.RequestHash == "" {
		c.RequestHash = domain.Hash(c.Payload)
	}
	_, err := tx.Exec(ctx, `INSERT INTO bots.command_outbox(id,tenant_id,bot_id,kind,ref_id,revision,request_hash,payload) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(id) DO NOTHING`, c.ID, c.TenantID, c.BotID, c.Kind, c.RefID, c.Revision, c.RequestHash, c.Payload)
	if err != nil {
		return err
	}
	var existing string
	if err := tx.QueryRow(ctx, `SELECT request_hash FROM bots.command_outbox WHERE id=$1`, c.ID).Scan(&existing); err != nil {
		return err
	}
	if existing != c.RequestHash {
		return domain.ErrConflict
	}
	return nil
}

// Recover is startup recovery for this single-process MVP. An unresolved send
// claim is never returned to pending; only known durable inbox commands replay.
func (s *Store) Recover(ctx context.Context, tenant string) error {
	if tenant == "" {
		return domain.ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE bots.inbox SET state=CASE WHEN run_id='' THEN 'pending' ELSE 'running' END,updated_at=now() WHERE state='processing' AND tenant_id=$1`, tenant); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO bots.audit_events(tenant_id,bot_id,scope_id,kind,ref_id,revision,actor,data) SELECT tenant_id,bot_id,scope_id,'delivery_unknown',id,task_revision,'recovery',jsonb_build_object('payload_hash',payload_hash,'reason','process stopped after send claim') FROM bots.deliveries WHERE tenant_id=$1 AND state='dispatching'`, tenant); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE bots.tasks t SET status='unknown' FROM bots.deliveries d WHERE d.task_id=t.id AND d.task_revision=t.revision AND d.state='dispatching' AND t.tenant_id=$1`, tenant); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE bots.deliveries SET state='unknown',error='process stopped after send claim; external result unconfirmed',updated_at=now() WHERE state='dispatching' AND tenant_id=$1`, tenant); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
