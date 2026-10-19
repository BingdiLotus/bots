#!/usr/bin/env python3
"""Bots G0 / real-model+sink gate against the actual Chronotope stack.

Starts the real Bots executable with real PG and synthetic signed Slack events.
Only the HTTP model (fake mode) and Slack provider are fixtures. No real Slack
messages are sent. Reconfigures api/worker/harness without deleting volumes;
restores their prior environment on exit. Requires an existing Chronotope stack.
"""
import argparse
import datetime as dt
import hashlib
import hmac
import json
import os
from pathlib import Path
import secrets
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request


ROOT = Path(__file__).resolve().parents[2]


def request(base, path, body=None, token="", method=None, headers=None, expected=(200,), timeout=15):
    sent_headers = {"Accept": "application/json", **(headers or {})}
    if token:
        sent_headers["Authorization"] = "Bearer " + token
    raw = None if body is None else json.dumps(body, ensure_ascii=False, separators=(",", ":")).encode()
    if raw is not None:
        sent_headers["Content-Type"] = "application/json"
    req = urllib.request.Request(base.rstrip("/") + path, data=raw, headers=sent_headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as response:
            status, data = response.status, response.read()
    except urllib.error.HTTPError as exc:
        status, data = exc.code, exc.read()
    result = json.loads(data) if data else {}
    if status not in expected:
        raise AssertionError(f"{req.method} {path}: HTTP {status}, expected {expected}")
    return result


def wait_for(label, fn, timeout=90):
    deadline, last = time.monotonic() + timeout, None
    while time.monotonic() < deadline:
        try:
            value = fn()
            if value:
                return value
        except (OSError, AssertionError) as exc:
            last = type(exc).__name__
        time.sleep(0.25)
    raise AssertionError(f"timed out waiting for {label}: {last}")


def stop(process, crash=False):
    if process and process.poll() is None:
        process.kill() if crash else process.terminate()
        try:
            process.wait(timeout=15)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=5)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--chronotope", type=Path, default=ROOT.parent / "chronotope")
    parser.add_argument("--api", default="http://127.0.0.1:8080")
    parser.add_argument("--mode", choices=("fake", "real"), default="fake")
    parser.add_argument("--model", default="claude-sonnet-4-6")
    parser.add_argument("--database-url", default=os.getenv("BOTS_E2E_DATABASE_URL", "postgres://chronotope:chronotope_dev@127.0.0.1:5432/chronotope?sslmode=disable"))
    parser.add_argument("--bots-port", type=int, default=18091)
    parser.add_argument("--fixture-port", type=int, default=18090)
    parser.add_argument("--timer-delay", type=int, default=45)
    parser.add_argument("--state-dir", type=Path)
    args = parser.parse_args()
    args.chronotope = args.chronotope.resolve()
    state_dir = args.state_dir or Path(tempfile.mkdtemp(prefix="bots-mvp-"))
    state_dir = state_dir.resolve()
    state_dir.mkdir(parents=True, exist_ok=True)
    print("Artifacts: " + str(state_dir), flush=True)
    # Fail a compilation check before changing any running service or starting
    # fixtures. The existing Chronotope stack is not needed for this build.
    binary = state_dir / "bots"
    build_env = os.environ.copy()
    build_env["GOTOOLCHAIN"] = "go1.26.0"
    subprocess.run(["go", "build", "-o", str(binary), "./cmd/bots"], cwd=ROOT, env=build_env, check=True)
    tenant, bot = "bots-e2e-" + secrets.token_hex(5), "team-bot"
    installation, channel = "slack-synthetic", "C_G0"
    scope = "scope_" + hashlib.sha256(json.dumps([tenant, bot, installation, channel], separators=(",", ":")).encode()).hexdigest()[:32]
    base, fixture_url = f"http://127.0.0.1:{args.bots_port}", f"http://127.0.0.1:{args.fixture_port}"
    docker_bots, docker_fixture = f"http://host.docker.internal:{args.bots_port}", f"http://host.docker.internal:{args.fixture_port}"
    env = os.environ.copy()
    # Test credentials are generated locally and never written into the report.
    env.update(API_AUTH_MODE="on", API_ADMIN_KEY=secrets.token_hex(24), APPLICATION_SERVICE_TOKEN=secrets.token_hex(24))
    env["APPLICATION_BRIDGES_JSON"] = json.dumps([{"ref": "bots-native", "tenant_id": tenant, "url": docker_bots + "/internal/tools", "token_env": "APPLICATION_SERVICE_TOKEN"}])
    env["APPLICATION_TIMER_TARGETS_JSON"] = json.dumps([{"ref": "bots-timers", "tenant_id": tenant, "subject_ref": bot, "scope_id": scope,
            "callback_url": docker_bots + "/internal/timer-fires", "token_env": "APPLICATION_SERVICE_TOKEN"}])
    env["HARNESS_FAKE_MODEL"] = "0"
    env["LITELLM_BASE_URL"] = docker_fixture + "/v1" if args.mode == "fake" else "http://litellm:4000"
    if args.mode == "fake":
        # Compose maps this host variable to harness.LITELLM_API_KEY. No-deps
        # below keeps the existing LiteLLM gateway itself unchanged.
        env["LITELLM_MASTER_KEY"] = "bots-fake-model-test-only"
    compose = ["docker", "compose"]
    if (args.chronotope / ".env").exists():
        compose += ["--env-file", str(args.chronotope / ".env")]
    compose += ["-f", str(args.chronotope / "deploy/docker-compose.yml")]
    prior = {}
    for name in ("api", "worker", "harness"):
        container = subprocess.check_output(compose + ["ps", "-q", name], cwd=args.chronotope, text=True).strip()
        if not container:
            raise RuntimeError("existing Chronotope " + name + " container required")
        raw_env = json.loads(subprocess.check_output(["docker", "inspect", "--format", "{{json .Config.Env}}", container], text=True))
        prior[name] = dict(v.split("=", 1) for v in raw_env)
    modified = {"API_AUTH_MODE", "API_ADMIN_KEY", "APPLICATION_SERVICE_TOKEN", "APPLICATION_BRIDGES_JSON", "APPLICATION_TIMER_TARGETS_JSON", "HARNESS_FAKE_MODEL", "LITELLM_BASE_URL", "LITELLM_MASTER_KEY"}
    checks, restarted = [], False
    fixture, application = None, None
    log = open(state_dir / "processes.log", "ab")
    app_env = env.copy()
    try:
        fixture = subprocess.Popen([sys.executable, str(ROOT / "test/fixtures/model-channel.py"), "--port", str(args.fixture_port), "--db", str(state_dir / "fixture.sqlite")], stdout=log, stderr=log)
        wait_for("fixture health", lambda: request(fixture_url, "/healthz"))
        subprocess.run(compose + ["up", "-d", "--no-build", "--no-deps", "api", "worker", "harness"], cwd=args.chronotope, env=env, check=True, stdout=log, stderr=log)
        wait_for("Chronotope health", lambda: request(args.api, "/healthz"))
        wait_for("worker registration", lambda: request("http://127.0.0.1:9070", "/deployments", {"uri": "http://worker:9080", "force": True, "use_http_11": True}, expected=(200, 201, 202)))
        app_env["BOTS_CHRONOTOPE_TOKEN"] = request(args.api, f"/orgs/{tenant}/keys", {}, token=env["API_ADMIN_KEY"], expected=(201,))["key"]
        app_env.update(BOTS_ADMIN_TOKEN=secrets.token_hex(24), SLACK_BOT_TOKEN="test-only-sink-token", SLACK_SIGNING_SECRET=secrets.token_hex(24),
            BOTS_DATABASE_URL=args.database_url, BOTS_HTTP_ADDR=f"0.0.0.0:{args.bots_port}", BOTS_CHRONOTOPE_URL=args.api,
            BOTS_SLACK_API_URL=fixture_url + "/api", BOTS_POLL_INTERVAL="100ms", BOTS_SEED_PATH=str(state_dir / "bot.yaml"),
            BOTS_RUNTIME_TOKEN_ENV="BOTS_CHRONOTOPE_TOKEN", BOTS_ADMIN_TOKEN_ENV="BOTS_ADMIN_TOKEN", BOTS_INTERNAL_TOKEN_ENV="APPLICATION_SERVICE_TOKEN")
        seed = {"tenant_id": tenant, "id": bot, "name": "G0 native team bot", "enabled": True, "account_enabled": True,
                "config": {"model": "bots-http-fixture" if args.mode == "fake" else args.model,
                    "instructions": "You are a test organization bot. CASE_REMEMBER: call remember and create_reminder in one turn. Store exactly 'G0 native knowledge: team review is Friday', kind knowledge. Use DUE as absolute due_at, reminder text 'G0 native reminder: team review'. Echo the actual knowledge_id in final. CASE_RECALL: call recall with query 'G0 native knowledge', echo exact stored ID and source. Never invent tool results.",
                    "timezone": "Asia/Shanghai", "channel_kind": "slack", "installation_id": installation, "workspace_id": "T_G0", "bot_user_id": "U_G0",
                    "channel_id": channel, "token_env": "SLACK_BOT_TOKEN", "signing_secret_env": "SLACK_SIGNING_SECRET", "bridge_ref": "bots-native", "timer_target_ref": "bots-timers",
                    "max_output_tokens": 1024, "max_steps": 8, "max_duration_ms": 180000, "max_pending_tasks": 10}}
        Path(app_env["BOTS_SEED_PATH"]).write_text(json.dumps(seed, ensure_ascii=False), encoding="utf-8")
        def start_app():
            process = subprocess.Popen([str(binary)], cwd=ROOT, env=app_env, stdout=log, stderr=log)
            wait_for("Bots admin", lambda: request(base, f"/api/tenants/{tenant}/bots/{bot}", token=app_env["BOTS_ADMIN_TOKEN"]))
            return process

        def records(kind):
            response = request(base, f"/api/tenants/{tenant}/bots/{bot}/" + kind, token=app_env["BOTS_ADMIN_TOKEN"])
            rows = response[kind]
            assert isinstance(rows, list), f"admin {kind} must contain a record array"
            return rows

        def state():
            return request(fixture_url, "/state")

        def runtime_snapshot():
            """Read authenticated runtime facts, including real-model calls.

            Fresh per-run tenant isolation makes all of its sessions relevant.
            Never treat the fake model's empty log as real-model evidence.
            """
            token = app_env["BOTS_CHRONOTOPE_TOKEN"]
            sessions = request(args.api, f"/orgs/{tenant}/sessions", token=token)["sessions"]
            run_ids = {entry["ref_id"] for entry in records("audit") if entry["kind"] == "run_final"}
            for session in sessions:
                timeline = request(args.api, "/sessions/" + session["id"], token=token)["recent_events"]
                # The current session view returns at most twenty events. Fail
                # closed if a fixture grows beyond that observation boundary.
                if len(timeline) >= 20:
                    raise RuntimeError("runtime timeline truncated; cannot establish complete call evidence")
                run_ids.update(entry["run_id"] for entry in timeline if entry.get("run_id"))
            calls = []
            for run_id in sorted(run_ids):
                run = request(args.api, "/runs/" + run_id, token=token)
                if run["status"] in ("failed", "canceled"):
                    failure = run.get("error") or {}
                    raise RuntimeError(f"runtime run {run_id} ended {run['status']}: {failure.get('code', 'unspecified')}")
                bound = run["bound"]
                if (bound.get("tenant_id"), bound.get("subject_ref"), bound.get("scope_id")) != (tenant, bot, scope):
                    raise RuntimeError("runtime evidence belongs to a different application binding")
                rows = request(args.api, "/runs/" + run_id + "/calls", token=token)["calls"] or []
                calls.extend({"run_id": run_id, "step": row["step"], "dispatch_seq": row["dispatch_seq"]}
                             for row in rows if row["kind"] == "llm")
            return {"sessions": sorted(session["id"] for session in sessions),
                    "runs": sorted(run_ids), "llm_calls": calls, "llm_call_count": len(calls)}

        def confirmed_deliveries(expected_count):
            # Also fail early when a runtime turn failed instead of waiting for
            # a success-shaped provider request that can never arrive.
            runtime_snapshot()
            sends, deliveries = state()["sends"], records("deliveries")
            if len(sends) > expected_count or len(deliveries) > expected_count:
                raise RuntimeError("unexpected duplicate provider call or delivery")
            for delivery in deliveries:
                if delivery["state"] in ("unknown", "rejected", "obsolete"):
                    raise RuntimeError("delivery cannot be confirmed: " + delivery["state"])
            if len(sends) != expected_count or len(deliveries) != expected_count:
                return None
            if any(delivery["state"] != "confirmed" or not delivery.get("provider_message_id") for delivery in deliveries):
                return None
            expected_receipts = {send["ts"] for send in sends}
            actual_receipts = {delivery["provider_message_id"] for delivery in deliveries}
            if len(expected_receipts) != expected_count or actual_receipts != expected_receipts:
                raise RuntimeError("local delivery receipts do not match the actual counting-sink responses")
            return deliveries

        def event(event_id, timestamp, text):
            payload = {"type": "event_callback", "team_id": "T_G0", "event_id": event_id,
                "event": {"type": "app_mention", "user": "U_TEST", "channel": channel, "text": "<@U_G0> " + text, "ts": timestamp, "event_ts": timestamp}}
            raw = json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode()
            stamp = str(int(time.time()))
            signature = "v0=" + hmac.new(app_env["SLACK_SIGNING_SECRET"].encode(), b"v0:" + stamp.encode() + b":" + raw, hashlib.sha256).hexdigest()
            return request(base, f"/channels/slack/{tenant}/{bot}/events", payload, headers={"X-Slack-Request-Timestamp": stamp, "X-Slack-Signature": signature})

        def passed(label):
            checks.append(label)
            print("PASS " + label, flush=True)

        application = start_app()
        # Real provider turns may take close to the configured three-minute run
        # deadline. Keep the reminder future long enough to prove waiting/restart.
        due = dt.datetime.now(dt.timezone.utc) + dt.timedelta(seconds=max(args.timer_delay, 300 if args.mode == "real" else 30))
        payload_text = "CASE_REMEMBER DUE=" + due.isoformat()
        ids = [event("EvG0Remember", "1792390000.000001", payload_text)["inbox_id"] for _ in range(10)]
        assert len(set(ids)) == 1
        wait_for("one final provider call with durable receipt", lambda: confirmed_deliveries(1),
                 timeout=210 if args.mode == "real" else 90)
        knowledge = records("knowledge")
        tasks = records("tasks")
        # Admin responses use the declared {kind: [...]} wrapper.
        assert len(knowledge) == 1 and len(tasks) == 1
        identity, task = knowledge[0]["id"], tasks[0]
        assert knowledge[0]["content"] == "G0 native knowledge: team review is Friday" and knowledge[0]["kind"] == "knowledge"
        assert task["text"] == "G0 native reminder: team review"
        wait_for("durable timer acceptance", lambda: records("tasks")[0]["timer_handle"])
        passed("ten signed source retries -> one native knowledge/task/final delivery")
        if args.mode == "fake":
            model_state = state()["models"]
            assert len(model_state) == 2 and model_state[-1]["verified"] == 1
            assert identity in json.loads(state()["sends"][0]["payload"])["text"]
            passed("full two-tool results, original call IDs and random persisted knowledge ID reach next actual model HTTP request")
        before_wait_models = len(state()["models"]) if args.mode == "fake" else None
        before_wait_runtime = runtime_snapshot()
        assert before_wait_runtime["llm_call_count"] > 0, "runtime call ledger did not observe the actual model turn"
        handle = records("tasks")[0]["timer_handle"]
        invocation = wait_for("pending Restate Sleep", lambda: sleep_evidence(handle, due))
        # No background model calls during timer waiting. Admin pause can accept
        # the fire but must prevent a new send claim.
        request(base, f"/api/tenants/{tenant}/bots/{bot}/enabled", {"enabled": False}, token=app_env["BOTS_ADMIN_TOKEN"], method="PUT")
        stop(application, crash=True)
        application = None
        subprocess.run(compose + ["restart", "worker"], cwd=args.chronotope, env=env, check=True, stdout=log, stderr=log)
        application = start_app()
        restarted = True
        assert request(base, f"/api/tenants/{tenant}/bots/{bot}", token=app_env["BOTS_ADMIN_TOKEN"])["enabled"] is False
        passed("Bots SIGKILL and worker restart while durable timer waits; PG/Restate volumes retained and admin pause preserved over YAML import")
        wait_for("paused fire accepted into due task", lambda: records("tasks")[0]["status"] == "due",
                 timeout=max(0, (due - dt.datetime.now(dt.timezone.utc)).total_seconds()) + 60)
        assert len(state()["sends"]) == 1
        paused_runtime = runtime_snapshot()
        assert paused_runtime == before_wait_runtime, "timer waiting dispatched new runtime model work"
        if args.mode == "fake":
            assert len(state()["models"]) == before_wait_models
        passed("paused Bot accepts durable fire without send or model call")
        request(base, f"/api/tenants/{tenant}/bots/{bot}/enabled", {"enabled": True}, token=app_env["BOTS_ADMIN_TOKEN"], method="PUT")
        wait_for("actual reminder sink receipt", lambda: records("tasks")[0]["status"] == "completed")
        assert len(state()["sends"]) == 2
        deliveries = wait_for("reminder durable receipt", lambda: confirmed_deliveries(2))
        resumed_runtime = runtime_snapshot()
        assert resumed_runtime == before_wait_runtime, "timer/resume dispatched new runtime model work"
        if args.mode == "fake":
            assert len(state()["models"]) == before_wait_models
        passed("resume -> one deterministic reminder, receipt/task completion persisted; no new model call")
        event("EvG0Recall", "1792390001.000001", "CASE_RECALL")
        wait_for("new thread recall reply and confirmed receipt", lambda: confirmed_deliveries(3),
                 timeout=210 if args.mode == "real" else 90)
        assert identity in json.loads(state()["sends"][-1]["payload"])["text"]
        audit = records("audit")
        runs = [a for a in audit if a["kind"] == "run_final"]
        assert len(runs) == 2
        passed("new runtime session after process restart recalls same scoped native knowledge and source")
        report = {"mode": args.mode, "real_channel": False, "tenant": tenant, "bot": bot, "scope": scope,
                  "checks": checks, "restart_verified": restarted, "task": records("tasks")[0], "deliveries": records("deliveries"),
                  "knowledge_id": identity, "sleep": invocation,
                  "model_http_requests": len(state()["models"]) if args.mode == "fake" else None,
                  "runtime_calls_before_wait": before_wait_runtime, "runtime_calls_while_paused": paused_runtime,
                  "runtime_calls_after_resume": resumed_runtime, "runtime_calls_after_recall": runtime_snapshot(), "audit": audit}
        (state_dir / "report.json").write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding="utf-8")
    finally:
        stop(application)
        stop(fixture)
        # Restore only the variables changed by this gate, using each running
        # service's original environment. Never echo captured credentials.
        for service in ("harness", "api", "worker"):
            restore_env = os.environ.copy()
            for key in modified:
                container_key = "LITELLM_API_KEY" if service == "harness" and key == "LITELLM_MASTER_KEY" else key
                restore_env[key] = prior[service].get(container_key, "")
            subprocess.run(compose + ["up", "-d", "--no-build", "--no-deps", service], cwd=args.chronotope, env=restore_env, check=False, stdout=log, stderr=log)
        log.close()


def sleep_evidence(handle, due):
    target = "application_timer/" + handle + "/run"
    rows = request("http://127.0.0.1:9070", "/query", {"query": "SELECT id,status FROM sys_invocation WHERE target='" + target.replace("'", "''") + "'"})["rows"]
    if len(rows) != 1 or rows[0]["status"] not in ("running", "suspended"):
        return None
    invocation = rows[0]
    entries = request("http://127.0.0.1:9070", "/query", {"query": "SELECT entry_json FROM sys_journal WHERE id='" + invocation["id"].replace("'", "''") + "' AND entry_type='Command: Sleep'"})["rows"]
    sleeps = [json.loads(row["entry_json"])["Command"]["Sleep"] for row in entries]
    if len(sleeps) != 1:
        return None
    wake = dt.datetime.fromtimestamp(sleeps[0]["wake_up_time"] / 1000, tz=dt.timezone.utc)
    assert abs((wake - due).total_seconds()) < 1
    assert wake > dt.datetime.now(dt.timezone.utc)
    return {"invocation_id": invocation["id"], "wake_up_time": sleeps[0]["wake_up_time"], "completion_id": sleeps[0]["completion_id"]}


if __name__ == "__main__":
    main()
