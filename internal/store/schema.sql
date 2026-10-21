CREATE SCHEMA IF NOT EXISTS bots;
CREATE TABLE IF NOT EXISTS bots.bots (
 tenant_id text NOT NULL, bot_id text NOT NULL, name text NOT NULL,
 enabled boolean NOT NULL, account_enabled boolean NOT NULL,
 revision integer NOT NULL DEFAULT 1, config_hash text NOT NULL,
 runtime_agent_id text NOT NULL DEFAULT '', config jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(tenant_id,bot_id)
);
CREATE TABLE IF NOT EXISTS bots.runtime_sessions (
 session_key text PRIMARY KEY, tenant_id text NOT NULL, bot_id text NOT NULL,
 scope_id text NOT NULL, revision integer NOT NULL, runtime_agent_id text NOT NULL,
 runtime_session_id text NOT NULL DEFAULT '', provider text NOT NULL,
 installation_id text NOT NULL, channel_id text NOT NULL, thread_id text NOT NULL,
 target jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(tenant_id,bot_id) REFERENCES bots.bots(tenant_id,bot_id),
 UNIQUE(tenant_id,bot_id,provider,installation_id,channel_id,thread_id)
);
CREATE TABLE IF NOT EXISTS bots.inbox (
 id text PRIMARY KEY, tenant_id text NOT NULL, bot_id text NOT NULL, scope_id text NOT NULL,
 namespace text NOT NULL, source_id text NOT NULL, request_hash text NOT NULL,
 command_id text NOT NULL DEFAULT '', session_key text NOT NULL DEFAULT '',
 input text NOT NULL DEFAULT '', target jsonb NOT NULL DEFAULT '{}',
 revision integer NOT NULL DEFAULT 0, runtime_session_id text NOT NULL DEFAULT '',
 run_id text NOT NULL DEFAULT '', state text NOT NULL, source_state text NOT NULL DEFAULT 'active', error text NOT NULL DEFAULT '',
 result jsonb, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(tenant_id,bot_id) REFERENCES bots.bots(tenant_id,bot_id),
 UNIQUE(tenant_id,bot_id,namespace,source_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS inbox_command_unique ON bots.inbox(tenant_id,bot_id,command_id) WHERE command_id<>'';
CREATE UNIQUE INDEX IF NOT EXISTS inbox_one_active_bot ON bots.inbox(tenant_id,bot_id) WHERE state IN ('processing','running');
CREATE INDEX IF NOT EXISTS inbox_pending ON bots.inbox(created_at,id) WHERE state='pending';
CREATE INDEX IF NOT EXISTS inbox_running ON bots.inbox(created_at,id) WHERE state='running';
CREATE TABLE IF NOT EXISTS bots.knowledge (
 id text PRIMARY KEY, tenant_id text NOT NULL, bot_id text NOT NULL, scope_id text NOT NULL,
 content text NOT NULL, kind text NOT NULL, source_id text NOT NULL, origin_command_id text NOT NULL,
 revision integer NOT NULL DEFAULT 1, active boolean NOT NULL DEFAULT true,
 created_at timestamptz NOT NULL DEFAULT now(), FOREIGN KEY(tenant_id,bot_id) REFERENCES bots.bots(tenant_id,bot_id)
);
CREATE INDEX IF NOT EXISTS knowledge_scope ON bots.knowledge(tenant_id,bot_id,scope_id,created_at DESC) WHERE active;
CREATE TABLE IF NOT EXISTS bots.memory_confirmations (
 token text PRIMARY KEY, tenant_id text NOT NULL, bot_id text NOT NULL, scope_id text NOT NULL,
 knowledge_id text NOT NULL, knowledge_revision integer NOT NULL, requested_command_id text NOT NULL,
 expires_at timestamptz NOT NULL, used_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(tenant_id,bot_id) REFERENCES bots.bots(tenant_id,bot_id)
);
CREATE INDEX IF NOT EXISTS memory_confirmations_expiry ON bots.memory_confirmations(expires_at);
CREATE TABLE IF NOT EXISTS bots.tasks (
 id text PRIMARY KEY, tenant_id text NOT NULL, bot_id text NOT NULL, scope_id text NOT NULL,
 origin_command_id text NOT NULL, reminder_slot integer NOT NULL DEFAULT 0,
 text text NOT NULL, due_at timestamptz NOT NULL, status text NOT NULL, revision integer NOT NULL DEFAULT 1,
 timer_id text NOT NULL, timer_handle text NOT NULL DEFAULT '', fire_id text NOT NULL DEFAULT '',
 delivery_id text NOT NULL DEFAULT '', target jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), FOREIGN KEY(tenant_id,bot_id) REFERENCES bots.bots(tenant_id,bot_id),
 UNIQUE(tenant_id,bot_id,origin_command_id,reminder_slot), UNIQUE(tenant_id,timer_id)
);
CREATE INDEX IF NOT EXISTS tasks_scope ON bots.tasks(tenant_id,bot_id,scope_id,created_at DESC);
CREATE TABLE IF NOT EXISTS bots.command_outbox (
 id text PRIMARY KEY, tenant_id text NOT NULL, bot_id text NOT NULL, kind text NOT NULL,
 ref_id text NOT NULL, revision integer NOT NULL, request_hash text NOT NULL, payload jsonb NOT NULL,
 handle text NOT NULL DEFAULT '', state text NOT NULL DEFAULT 'pending', error text NOT NULL DEFAULT '',
 attempts integer NOT NULL DEFAULT 0, next_attempt_at timestamptz NOT NULL DEFAULT now(),
 created_at timestamptz NOT NULL DEFAULT now(), FOREIGN KEY(tenant_id,bot_id) REFERENCES bots.bots(tenant_id,bot_id)
);
CREATE INDEX IF NOT EXISTS command_pending ON bots.command_outbox(next_attempt_at,created_at) WHERE state='pending';
CREATE TABLE IF NOT EXISTS bots.deliveries (
 id text PRIMARY KEY, operation_id text NOT NULL UNIQUE, tenant_id text NOT NULL, bot_id text NOT NULL,
 scope_id text NOT NULL, task_id text NOT NULL DEFAULT '', task_revision integer NOT NULL DEFAULT 0,
 command_id text NOT NULL DEFAULT '', target jsonb NOT NULL, text text NOT NULL, payload_hash text NOT NULL,
 state text NOT NULL DEFAULT 'pending', provider_message_id text NOT NULL DEFAULT '', error text NOT NULL DEFAULT '',
 attempts integer NOT NULL DEFAULT 0, next_attempt_at timestamptz NOT NULL DEFAULT now(),
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(tenant_id,bot_id) REFERENCES bots.bots(tenant_id,bot_id)
);
CREATE INDEX IF NOT EXISTS delivery_pending ON bots.deliveries(next_attempt_at,created_at) WHERE state IN ('pending','retryable');
CREATE INDEX IF NOT EXISTS delivery_scope ON bots.deliveries(tenant_id,bot_id,scope_id,created_at DESC);
CREATE TABLE IF NOT EXISTS bots.audit_events (
 id bigserial PRIMARY KEY, tenant_id text NOT NULL, bot_id text NOT NULL, scope_id text NOT NULL DEFAULT '',
 kind text NOT NULL, ref_id text NOT NULL, revision integer NOT NULL DEFAULT 0,
 actor text NOT NULL, data jsonb NOT NULL DEFAULT '{}', created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(tenant_id,bot_id) REFERENCES bots.bots(tenant_id,bot_id)
);
CREATE INDEX IF NOT EXISTS audit_scope ON bots.audit_events(tenant_id,bot_id,scope_id,id DESC);
CREATE UNIQUE INDEX IF NOT EXISTS audit_config_revision ON bots.audit_events(tenant_id,bot_id,revision) WHERE kind='config_revision';
