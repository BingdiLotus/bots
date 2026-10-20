#!/usr/bin/env python3
"""G2 paired SOP evaluation against the real model and an isolated sink.

Baseline and candidate run as separate test tenants, each with an equivalent
initial knowledge set and its own counting Slack sink target. Only instructions
differ. The frozen suite/checker are loaded after candidate.json exists and are
verified against frozen.json; they are never part of the proposal prompt.

This is a local evaluation harness, not a product evaluation service. It does
not write to the organization Bot or call real Slack. It reconfigures the
existing Chronotope api/worker/harness for the test tenants and restores their
prior environment in finally.
"""
import argparse
import datetime as dt
import hashlib
import hmac
import json
import os
import secrets
import signal
import subprocess
import sys
import time
from pathlib import Path

import psycopg
import requests
from psycopg.rows import dict_row

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "test/sop"))
import checker  # noqa: E402


class FatalError(Exception):
    pass


def http(method, url, token="", body=None, headers=None, expected=(200,), timeout=30):
    sent = {"Accept": "application/json", **(headers or {})}
    if token:
        sent["Authorization"] = "Bearer " + token
    response = requests.request(method, url, json=body, headers=sent, timeout=timeout)
    if response.status_code not in expected:
        raise RuntimeError(f"{method} {url.split('?')[0]} -> {response.status_code}: {response.text[:400]}")
    return response.json() if response.content else {}


def wait_for(label, fn, timeout=180):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        try:
            value = fn()
            if value:
                return value
        except (OSError, AssertionError, psycopg.Error, requests.RequestException, RuntimeError) as exc:
            last = type(exc).__name__
        time.sleep(0.25)
    raise RuntimeError(f"timed out waiting for {label}: {last}")


def sha(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def scope_id(tenant, bot, installation, channel):
    raw = json.dumps([tenant, bot, installation, channel], separators=(",", ":"), ensure_ascii=False).encode()
    return "scope_" + hashlib.sha256(raw).hexdigest()[:32]


def iso(value):
    return value.isoformat() if hasattr(value, "isoformat") else value


class DB:
    def __init__(self, dsn):
        self.conn = psycopg.connect(dsn, autocommit=True, row_factory=dict_row)

    def all(self, sql, params=()):
        return self.conn.execute(sql, params).fetchall()

    def one(self, sql, params=()):
        rows = self.conn.execute(sql, params).fetchall()
        return rows[0] if rows else None

    def exec(self, sql, params=()):
        self.conn.execute(sql, params)

    def close(self):
        self.conn.close()


class Arm:
    def __init__(self, name, tenant, port, channel, secret, key, seed_path, process):
        self.name = name
        self.tenant = tenant
        self.port = port
        self.channel = channel
        self.secret = secret
        self.key = key
        self.seed_path = seed_path
        self.process = process


def compose(chronotope):
    cmd = ["docker", "compose"]
    if (chronotope / ".env").exists():
        cmd += ["--env-file", str(chronotope / ".env")]
    return cmd + ["-f", str(chronotope / "deploy/docker-compose.yml")]


def prior_env(compose_cmd, name):
    container = subprocess.check_output(compose_cmd + ["ps", "-q", name], text=True).strip()
    if not container:
        raise RuntimeError("existing Chronotope " + name + " container required")
    raw = json.loads(subprocess.check_output(["docker", "inspect", "--format", "{{json .Config.Env}}", container], text=True))
    return dict(item.split("=", 1) for item in raw)


def restore(compose_cmd, prior, log):
    modified = {"API_AUTH_MODE", "API_ADMIN_KEY", "APPLICATION_SERVICE_TOKEN", "APPLICATION_BRIDGES_JSON",
                "APPLICATION_TIMER_TARGETS_JSON", "HARNESS_FAKE_MODEL", "LITELLM_BASE_URL"}
    for service in ("harness", "api", "worker"):
        env = os.environ.copy()
        for key in modified:
            env[key] = prior[service].get(key, "")
        subprocess.run(compose_cmd + ["up", "-d", "--no-build", "--no-deps", service],
                       env=env, stdout=log, stderr=log, check=False)


def start_bots(binary, port, seed_path, key, admin, signing, sink, dsn, service_token, state_dir, name, log):
    env = os.environ.copy()
    env.update({
        "BOTS_DATABASE_URL": dsn,
        "BOTS_HTTP_ADDR": f"0.0.0.0:{port}",
        "BOTS_SEED_PATH": str(seed_path),
        "BOTS_CHRONOTOPE_URL": "http://127.0.0.1:8080",
        "BOTS_SLACK_API_URL": sink + "/api",
        "BOTS_POLL_INTERVAL": "100ms",
        "BOTS_RUNTIME_TOKEN_ENV": "BOTS_CHRONOTOPE_TOKEN",
        "BOTS_ADMIN_TOKEN_ENV": "BOTS_ADMIN_TOKEN",
        "BOTS_INTERNAL_TOKEN_ENV": "APPLICATION_SERVICE_TOKEN",
        "BOTS_CHRONOTOPE_TOKEN": key,
        "BOTS_ADMIN_TOKEN": admin,
        "APPLICATION_SERVICE_TOKEN": service_token,
        "SLACK_BOT_TOKEN": "test-only-sink-token",
        "SLACK_SIGNING_SECRET": signing,
    })
    process = subprocess.Popen([str(binary)], cwd=ROOT, env=env, stdout=log, stderr=log)
    return process


def event_payload(tenant, channel, case):
    timestamp = case["message_ts"]
    event_id = "sop_" + case["id"] + "_" + secrets.token_hex(4)
    return event_id, {
        "type": "event_callback", "team_id": "T_SOP", "event_id": event_id,
        "event": {"type": "app_mention", "user": "U_HUMAN", "channel": channel,
                  "text": "<@U_SOP> " + case["input"], "ts": timestamp, "event_ts": timestamp},
    }


def send_event(arm, case):
    event_id, payload = event_payload(arm.tenant, arm.channel, case)
    raw = json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode()
    stamp = str(int(time.time()))
    signature = "v0=" + hmac.new(arm.secret.encode(), b"v0:" + stamp.encode() + b":" + raw, hashlib.sha256).hexdigest()
    response = requests.post(f"http://127.0.0.1:{arm.port}/channels/slack/{arm.tenant}/sop-bot/events", data=raw,
                             headers={"Content-Type": "application/json", "X-Slack-Request-Timestamp": stamp,
                                      "X-Slack-Signature": signature}, timeout=30)
    if response.status_code != 200:
        raise RuntimeError(f"event admission {event_id}: {response.status_code} {response.text[:300]}")
    body = response.json()
    if not body.get("accepted") and body.get("reason") != "disabled":
        raise RuntimeError(f"event ignored: {body}")
    return body.get("inbox_id")


def counts(db, tenant):
    return {
        "knowledge": db.one("SELECT count(*) AS n FROM bots.knowledge WHERE tenant_id=%s AND active", (tenant,))["n"],
        "tasks": db.one("SELECT count(*) AS n FROM bots.tasks WHERE tenant_id=%s", (tenant,))["n"],
    }


def wait_final(db, tenant, thread, timeout=240):
    def ready():
        row = db.one("""SELECT id,text,state,provider_message_id,created_at FROM bots.deliveries
                        WHERE tenant_id=%s AND bot_id='sop-bot' AND id LIKE 'delivery_final_%%'
                          AND target->>'thread_id'=%s ORDER BY created_at DESC LIMIT 1""", (tenant, thread))
        if row and row["state"] == "confirmed" and row["text"]:
            return row
        if row and row["state"] in ("unknown", "rejected", "obsolete"):
            raise FatalError("final delivery ended " + row["state"])
        return None
    return wait_for("confirmed final delivery", ready, timeout)


def wait_inbox(db, inbox_id, timeout=240):
    def done():
        row = db.one("SELECT * FROM bots.inbox WHERE id=%s", (inbox_id,))
        if row and row["state"] in ("complete", "failed", "blocked"):
            return row
        return None
    return wait_for("inbox terminal", done, timeout)


def task_for(db, tenant, thread):
    return db.one("""SELECT id,text,due_at,status,target,delivery_id FROM bots.tasks
                     WHERE tenant_id=%s AND bot_id='sop-bot' AND target->>'thread_id'=%s
                     ORDER BY created_at DESC LIMIT 1""", (tenant, thread))


def tools_for(api, key, session_id, run_id):
    if not session_id:
        return [], None
    session = http("GET", api + "/sessions/" + session_id, key)
    events = session.get("recent_events", [])
    tools = [event["payload"].get("name") for event in events
             if event["type"] == "tool.call" and event.get("run_id") == run_id]
    return tools, session


def runtime_run(api, key, run_id):
    if not run_id:
        return None
    return http("GET", api + "/runs/" + run_id, key)


def sink_matches(sink, channel, thread):
    state = http("GET", sink + "/state")
    found = []
    for send in state.get("sends", []):
        payload = json.loads(send["payload"])
        if payload.get("channel") == channel and payload.get("thread_ts") == thread:
            found.append({"ts": send["ts"], "text": payload.get("text", ""), "client_msg_id": payload.get("client_msg_id")})
    return found


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--chronotope", type=Path, default=ROOT.parent / "chronotope")
    parser.add_argument("--api", default="http://127.0.0.1:8080")
    parser.add_argument("--database-url", default=os.getenv("BOTS_E2E_DATABASE_URL", "postgres://chronotope:chronotope_dev@127.0.0.1:5432/chronotope?sslmode=disable"))
    parser.add_argument("--model", default="claude-sonnet-4-6")
    parser.add_argument("--sink-port", type=int, default=18190)
    parser.add_argument("--baseline-port", type=int, default=18191)
    parser.add_argument("--candidate-port", type=int, default=18192)
    parser.add_argument("--timeout", type=int, default=240)
    parser.add_argument("--only", help="comma-separated case ids for a setup smoke; omit for the full suite")
    parser.add_argument("--state-dir", type=Path)
    args = parser.parse_args()
    args.chronotope = args.chronotope.resolve()
    state_dir = (args.state_dir or Path("/tmp/bots-sop-" + secrets.token_hex(4))).resolve()
    state_dir.mkdir(parents=True, exist_ok=True)
    log = open(state_dir / "processes.log", "ab")
    suite = json.load(open(ROOT / "test/sop/suite.json"))
    frozen = json.load(open(ROOT / "test/sop/frozen.json"))
    baseline = json.load(open(ROOT / "test/sop/baseline.json"))
    candidate = json.load(open(ROOT / "test/sop/candidate.json"))
    if sha(ROOT / "test/sop/suite.json") != frozen["suite_sha256"] or sha(ROOT / "test/sop/checker.py") != frozen["checker_sha256"]:
        raise SystemExit("suite/checker changed after frozen.json was written")
    if candidate.get("frozen_suite_used_by_proposal") is not False:
        raise SystemExit("candidate was not generated without the frozen suite context")
    code_commit = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
    product_code_commit = subprocess.check_output(["git", "log", "-1", "--format=%H", "--", "cmd", "internal"], cwd=ROOT, text=True).strip()
    dirty = bool(subprocess.check_output(["git", "status", "--porcelain"], cwd=ROOT, text=True).strip())
    suffix = secrets.token_hex(4)
    arm_specs = [
        ("baseline", f"bots-sop-{suffix}-base", args.baseline_port, "C_SOP_BASE", baseline["config"]["instructions"]),
        ("candidate", f"bots-sop-{suffix}-cand", args.candidate_port, "C_SOP_CAND", candidate["candidate_instructions"]),
    ]
    bot, installation, workspace, bot_user = "sop-bot", "sop-synthetic", "T_SOP", "U_SOP"
    base_url = args.api
    sink = f"http://127.0.0.1:{args.sink_port}"
    compose_cmd = compose(args.chronotope)
    prior = {name: prior_env(compose_cmd, name) for name in ("api", "worker", "harness")}
    service_token = secrets.token_hex(24)
    admin_key = secrets.token_hex(24)
    env = os.environ.copy()
    env.update({"API_AUTH_MODE": "on", "API_ADMIN_KEY": admin_key, "APPLICATION_SERVICE_TOKEN": service_token,
                "HARNESS_FAKE_MODEL": "0", "LITELLM_BASE_URL": "http://litellm:4000"})
    bridges, timers = [], []
    for name, tenant, port, channel, _ in arm_specs:
        bridges.append({"ref": name + "-bridge", "tenant_id": tenant,
                        "url": f"http://host.docker.internal:{port}/internal/tools", "token_env": "APPLICATION_SERVICE_TOKEN"})
        timers.append({"ref": name + "-timer", "tenant_id": tenant, "subject_ref": bot,
                       "scope_id": scope_id(tenant, bot, installation, channel),
                       "callback_url": f"http://host.docker.internal:{port}/internal/timer-fires",
                       "token_env": "APPLICATION_SERVICE_TOKEN"})
    env["APPLICATION_BRIDGES_JSON"] = json.dumps(bridges, separators=(",", ":"))
    env["APPLICATION_TIMER_TARGETS_JSON"] = json.dumps(timers, separators=(",", ":"))
    fixture = None
    processes = []
    arms = {}
    report = {
        "version": 1, "generated_at": dt.datetime.now().astimezone().isoformat(timespec="seconds"),
        "only_cases": args.only,
        "mode": "real-model isolated sink", "code_commit": code_commit, "product_code_commit": product_code_commit,
        "code_dirty": dirty,
        "model": args.model, "source_trace": json.load(open(ROOT / "test/sop/source.json")),
        "baseline": {"revision": baseline["revision"], "config_hash": baseline["config_hash"],
                     "runtime_agent_id": baseline["runtime_agent_id"],
                     "instructions_sha256": hashlib.sha256(baseline["config"]["instructions"].encode()).hexdigest()},
        "candidate": {key: candidate[key] for key in ("proposal_run_id", "proposal_agent_id", "proposal_session_id",
                                                       "proposal_model", "proposal_usage", "patch", "hypothesis",
                                                       "candidate_instructions_sha256", "proposal_prompt_sha256")},
        "environment": {"chronotope_api": base_url, "provider": "litellm", "model_alias": args.model,
                        "model_upstream_version": "unknown", "harness": "existing Chronotope harness",
                        "tool_schema_sha256": None},
        "fixtures": {"suite_sha256": frozen["suite_sha256"], "checker_sha256": frozen["checker_sha256"],
                     "frozen_at": frozen["frozen_at"], "initial_knowledge": suite["initial_knowledge"],
                     "note": "suite/checker frozen before candidate generation"},
        "results": {}, "reminders": {}, "cost": {}, "decision": None, "publish": None, "feedback": None,
        "arms": {name: {"tenant": tenant, "channel": channel, "port": port} for name, tenant, port, channel, _ in arm_specs},
        "notes": ["separate isolated tenants/identities/channels; only instructions differ among behavior settings",
                  "no real Slack credentials reach either arm; the sink is a counting test provider",
                  "proposal run is in the real tenant but has no tools or Bots state"],
        "errors": [],
    }
    try:
        fixture = subprocess.Popen([sys.executable, str(ROOT / "test/fixtures/model-channel.py"),
                                    "--port", str(args.sink_port), "--db", str(state_dir / "sink.sqlite"),
                                    "--runtime-api", args.api], stdout=log, stderr=log)
        processes.append(fixture)
        wait_for("sink health", lambda: http("GET", sink + "/healthz"))
        subprocess.run(compose_cmd + ["up", "-d", "--no-build", "--no-deps", "api", "worker", "harness"],
                       cwd=str(args.chronotope), env=env, check=True, stdout=log, stderr=log)
        wait_for("Chronotope health", lambda: http("GET", base_url + "/healthz"))
        wait_for("worker registration", lambda: http("POST", "http://127.0.0.1:9070/deployments",
                                                      body={"uri": "http://worker:9080", "force": True, "use_http_11": True},
                                                      expected=(200, 201, 202)))
        keys = {}
        for name, tenant, _, _, _ in arm_specs:
            keys[tenant] = http("POST", base_url + f"/orgs/{tenant}/keys", admin_key, body={}, expected=(201,))["key"]
        binary = state_dir / "bots"
        build_env = os.environ.copy()
        build_env["GOTOOLCHAIN"] = "go1.26.0"
        subprocess.run(["go", "build", "-o", str(binary), "./cmd/bots"], cwd=ROOT, env=build_env, check=True, stdout=log, stderr=log)
        for name, tenant, port, channel, instructions in arm_specs:
            seed = {"tenant_id": tenant, "id": bot, "name": "G2 " + name, "enabled": True, "account_enabled": True,
                    "config": {"model": args.model, "timezone": "Asia/Shanghai", "instructions": instructions,
                               "channel_kind": "slack", "event_transport": "webhook", "installation_id": installation,
                               "workspace_id": workspace, "bot_user_id": bot_user, "channel_id": channel,
                               "token_env": "SLACK_BOT_TOKEN", "signing_secret_env": "SLACK_SIGNING_SECRET",
                               "bridge_ref": name + "-bridge", "timer_target_ref": name + "-timer",
                               "max_output_tokens": 1024, "max_steps": 8, "max_duration_ms": 180000, "max_pending_tasks": 10}}
            seed_path = state_dir / (name + "-seed.json")
            seed_path.write_text(json.dumps(seed, ensure_ascii=False), encoding="utf-8")
            signing = secrets.token_hex(32)
            admin = secrets.token_hex(24)
            process = start_bots(binary, port, seed_path, keys[tenant], admin, signing, sink, args.database_url, service_token, state_dir, name, log)
            processes.append(process)
            wait_for(name + " admin", lambda p=port, a=admin: http("GET", f"http://127.0.0.1:{p}/api/tenants/" + tenant + "/bots/" + bot, a))
            arms[name] = Arm(name, tenant, port, channel, signing, keys[tenant], seed_path, process)
            arms[name].admin = admin
        db = DB(args.database_url)
        report["initial_state"] = {}
        for name, arm in arms.items():
            seeded = []
            for item in suite["initial_knowledge"]:
                # Knowledge IDs are globally unique in the shared PG schema;
                # derive isolated IDs while keeping identical content/source.
                knowledge_id = "knowledge_" + hashlib.sha256((arm.tenant + ":" + item["id"]).encode()).hexdigest()[:32]
                db.exec("""INSERT INTO bots.knowledge
                           (id,tenant_id,bot_id,scope_id,content,kind,source_id,origin_command_id,revision,active)
                           VALUES(%s,%s,%s,%s,%s,%s,%s,%s,1,true)""",
                        (knowledge_id, arm.tenant, bot, scope_id(arm.tenant, bot, installation, arm.channel),
                         item["content"], item["kind"], item["source_id"], item["origin_command_id"]))
                seeded.append({"id": knowledge_id, "content": item["content"], "source_id": item["source_id"]})
            report["initial_state"][name] = seeded
        cases = [case for case in suite["cases"] if not args.only or case["id"] in args.only.split(",")]
        for case in cases:
            report["results"][case["id"]] = {}
            for name, arm in arms.items():
                before = counts(db, arm.tenant)
                started = dt.datetime.now(dt.timezone.utc)
                try:
                    inbox_id = send_event(arm, case)
                    delivery = wait_final(db, arm.tenant, case["message_ts"], args.timeout)
                    inbox = wait_inbox(db, inbox_id, args.timeout)
                    after = counts(db, arm.tenant)
                    task = task_for(db, arm.tenant, case["message_ts"]) if after["tasks"] > before["tasks"] else None
                    run = runtime_run(args.api, arm.key, inbox.get("run_id"))
                    calls = http("GET", args.api + "/runs/" + inbox["run_id"] + "/calls", arm.key).get("calls", []) if inbox.get("run_id") else []
                    tools, session = tools_for(args.api, arm.key, inbox.get("runtime_session_id"), inbox.get("run_id"))
                    sink_rows = sink_matches(sink, arm.channel, case["message_ts"])
                    observed = {
                        "final": delivery["text"], "tools": tools, "knowledge_delta": after["knowledge"] - before["knowledge"],
                        "task_delta": after["tasks"] - before["tasks"], "task": task,
                        "accepted_at": iso(inbox["created_at"]), "channel": arm.channel, "thread": case["message_ts"],
                    }
                    failures = checker.check_case(case, observed)
                    if not run or not inbox.get("run_id"):
                        failures.append("missing canonical runtime run")
                    else:
                        bound = run.get("bound", {})
                        if (bound.get("tenant_id"), bound.get("subject_ref"), bound.get("scope_id")) != (
                                arm.tenant, bot, scope_id(arm.tenant, bot, installation, arm.channel)):
                            failures.append("runtime binding mismatch")
                    if session is not None and len(session.get("recent_events", [])) >= 20:
                        failures.append("runtime timeline truncated")
                    if not sink_rows or delivery["provider_message_id"] not in {row["ts"] for row in sink_rows}:
                        failures.append("confirmed delivery has no matching sink receipt")
                    result = {
                        "pass": not failures, "failures": failures, "final": delivery["text"], "tools": tools,
                        "knowledge_delta": observed["knowledge_delta"], "task_delta": observed["task_delta"],
                        "task": {key: (iso(task[key]) if key == "due_at" else task[key]) for key in ("id", "text", "due_at", "status", "delivery_id")} if task else None,
                        "task_target": task["target"] if task else None,
                        "run_id": inbox.get("run_id"), "runtime_session_id": inbox.get("runtime_session_id"),
                        "runtime_status": run.get("status") if run else None,
                        "runtime_usage": run.get("usage") if run else None,
                        "runtime_calls": calls, "llm_call_count": sum(1 for call in calls if call.get("kind") == "llm"),
                        "sink_receipts": sink_rows, "duration_seconds": round((dt.datetime.now(dt.timezone.utc) - started).total_seconds(), 2),
                    }
                    if run and run.get("bound", {}).get("agent_config", {}).get("application_tools"):
                        report["environment"]["tool_schema_sha256"] = hashlib.sha256(json.dumps(
                            run["bound"]["agent_config"]["application_tools"], sort_keys=True, ensure_ascii=False).encode()).hexdigest()
                    report["results"][case["id"]][name] = result
                except Exception as exc:  # preserve the failure, stop this arm's remaining cases
                    report["results"][case["id"]][name] = {"pass": False, "error": type(exc).__name__ + ": " + str(exc)[:500],
                                                           "duration_seconds": round((dt.datetime.now(dt.timezone.utc) - started).total_seconds(), 2)}
                    report["errors"].append({"case": case["id"], "arm": name, "error": str(exc)[:500]})
                    for remaining in cases[cases.index(case) + 1:]:
                        report["results"].setdefault(remaining["id"], {})[name] = {"pass": False, "skipped": "prior case failed"}
                    break
            if any("error" in result for result in (report["results"][case["id"]].get(name, {}) for name in arms)):
                break
        # Wait for the relative reminder to reach the sink, proving timer + SOP
        # did not create an unconfirmed or repeated reminder.
        for name, arm in arms.items():
            task = task_for(db, arm.tenant, suite["cases"][3]["message_ts"])
            if not task:
                report["reminders"][name] = {"error": "task missing"}
                continue
            def reminder_ready(task_id=task["id"]):
                current = db.one("SELECT status,delivery_id FROM bots.tasks WHERE id=%s", (task_id,))
                if current and current["status"] == "completed" and current["delivery_id"]:
                    return current
                if current and current["status"] in ("canceled", "unknown", "obsolete"):
                    raise RuntimeError("task ended " + current["status"])
                return None
            try:
                current = wait_for("reminder delivery", reminder_ready, 300)
                delivery = db.one("SELECT id,state,text,provider_message_id FROM bots.deliveries WHERE id=%s", (current["delivery_id"],))
                report["reminders"][name] = {"task_id": task["id"], "state": delivery["state"], "text": delivery["text"],
                                              "provider_message_id": delivery["provider_message_id"],
                                              "sink_receipts": sink_matches(sink, arm.channel, suite["cases"][3]["message_ts"])}
            except Exception as exc:
                report["reminders"][name] = {"error": str(exc)[:300]}
        # Cost is based on actual Chronotope call ledgers, not estimates.
        for name, arm in arms.items():
            calls = 0
            tokens = 0
            for case_result in report["results"].values():
                run = case_result.get(name, {})
                calls += run.get("llm_call_count", 0)
                tokens += (run.get("runtime_usage") or {}).get("tokens_total", 0)
            report["cost"][name] = {"llm_calls": calls, "tokens_total_from_runs": tokens,
                                    "proposal_cost_excluded": True}
        report["cost"]["proposal"] = {"run_id": candidate.get("proposal_run_id"),
                                      "usage": candidate.get("proposal_usage"),
                                      "excluded_from_eval_arm_totals": True}
        target = report["results"].get("trace_recall", {})
        target_base, target_cand = target.get("baseline", {}), target.get("candidate", {})
        guards = ["single_recall", "missing_recall", "reminder_relative", "ambiguous_time"]
        guard_failures = {case: report["results"].get(case, {}).get("candidate", {}) for case in guards}
        report["summary"] = {
            "target_baseline_pass": target_base.get("pass"),
            "target_candidate_pass": target_cand.get("pass"),
            "target_baseline_failures": target_base.get("failures", []),
            "target_candidate_failures": target_cand.get("failures", []),
            "candidate_guard_failures": {case: value.get("failures", ["missing"]) for case, value in guard_failures.items() if not value.get("pass")},
            "paired": {
                "target_improvement": bool(target_base.get("pass") is False and target_cand.get("pass") is True),
                "candidate_only_guard_failures": [
                    case for case in guards
                    if report["results"].get(case, {}).get("baseline", {}).get("pass") is True
                    and report["results"].get(case, {}).get("candidate", {}).get("pass") is False
                ],
                "shared_guard_failures": [
                    case for case in guards
                    if report["results"].get(case, {}).get("baseline", {}).get("pass") is False
                    and report["results"].get(case, {}).get("candidate", {}).get("pass") is False
                ],
                "baseline_only_guard_failures": [
                    case for case in guards
                    if report["results"].get(case, {}).get("baseline", {}).get("pass") is False
                    and report["results"].get(case, {}).get("candidate", {}).get("pass") is True
                ],
            },
            "all_arms_completed": all(
                report["results"].get(case, {}).get(name, {}).get("pass") is not None
                for case in ("trace_recall", "single_recall", "missing_recall", "reminder_relative", "ambiguous_time")
                for name in arms),
        }
        (ROOT / "test/sop/report.json").write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding="utf-8")
        print(json.dumps(report["summary"], ensure_ascii=False, indent=2))
        print("report: test/sop/report.json")
    finally:
        for process in reversed(processes):
            if process and process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=15)
                except subprocess.TimeoutExpired:
                    process.kill()
        restore(compose_cmd, prior, log)
        log.close()


if __name__ == "__main__":
    main()
