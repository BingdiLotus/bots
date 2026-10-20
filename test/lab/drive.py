#!/usr/bin/env python3
"""Seed and observe a test-only multi-bot Slack chain.

The driver posts as Lab B, mentions Lab A, then waits for both lab inboxes to
advance. The bots continue from peer app_mentions; BOTS_TEST_MAX_PEER_ROUNDS
in each lab process caps the chain. It never prints Slack/tenant credentials.

Usage:
  python3 test/lab/drive.py
  python3 test/lab/drive.py --channel C... --target-user U... --max-wait 90
"""

import argparse
import json
import os
import pathlib
import subprocess
import sys
import time
import urllib.request

DEFAULT_CHANNEL = "REDACTED_LAB_CHANNEL"
DEFAULT_TARGET = "REDACTED_BOT_USER_A"
DEFAULT_DRIVER_ENV = pathlib.Path.home() / ".config/bots/lab-b/env"
DEFAULT_REPO_ENV = pathlib.Path(__file__).resolve().parents[2] / ".env"


def load_env(path):
    values = {}
    for line in pathlib.Path(path).read_text().splitlines():
        line = line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        values[key] = value
    return values


def slack_post(token, channel, text, thread_ts=None):
    payload = {"channel": channel, "text": text}
    if thread_ts:
        payload["thread_ts"] = thread_ts
    body = json.dumps(payload).encode()
    request = urllib.request.Request(
        "https://slack.com/api/chat.postMessage",
        data=body,
        headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"},
        method="POST",
    )
    last = None
    for _ in range(3):
        try:
            with urllib.request.urlopen(request, timeout=30) as response:
                payload = json.load(response)
            if not payload.get("ok"):
                raise RuntimeError("Slack chat.postMessage: " + str(payload.get("error")))
            return payload["ts"]
        except Exception as exc:  # transient TLS/API errors are retried
            last = exc
            time.sleep(2)
    raise RuntimeError("Slack chat.postMessage failed: " + type(last).__name__)


def counts(dsn, tenants=("bots-lab-a", "bots-lab-b")):
    quoted = ",".join("'" + t + "'" for t in tenants)
    sql = (
        "select tenant_id||'|'||count(*) from bots.inbox where tenant_id in ("
        + quoted
        + ") group by tenant_id"
    )
    out = subprocess.check_output(["psql", dsn, "-Atc", sql], text=True).strip()
    result = {t: 0 for t in tenants}
    for line in out.splitlines():
        tenant, count = line.split("|", 1)
        result[tenant] = int(count)
    return result


def deliveries(dsn, tenants=("bots-lab-a", "bots-lab-b")):
    quoted = ",".join("'" + t + "'" for t in tenants)
    sql = (
        "select tenant_id||'|'||state||'|'||coalesce(provider_message_id,'')||'|'||left(text,100) "
        "from bots.deliveries where tenant_id in (" + quoted + ") order by created_at desc limit 6"
    )
    return subprocess.check_output(["psql", dsn, "-Atc", sql], text=True).strip().splitlines()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--channel", default=DEFAULT_CHANNEL)
    parser.add_argument("--target-user", default=DEFAULT_TARGET)
    parser.add_argument("--driver-env", default=str(DEFAULT_DRIVER_ENV))
    parser.add_argument("--repo-env", default=str(DEFAULT_REPO_ENV))
    parser.add_argument("--max-wait", type=int, default=120)
    parser.add_argument("--attempts", type=int, default=3)
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()

    driver = load_env(args.driver_env)
    repo = load_env(args.repo_env)
    token = driver.get("SLACK_BOT_TOKEN_B")
    dsn = repo.get("BOTS_DATABASE_URL")
    if not token or not dsn:
        raise SystemExit("missing lab B token or Bots database URL")
    budget_a = load_env(pathlib.Path.home() / ".config/bots/lab-a/env").get("BOTS_TEST_MAX_PEER_ROUNDS", "0")
    budget_b = driver.get("BOTS_TEST_MAX_PEER_ROUNDS", "0")
    prompt = (
        "<@" + args.target_user + "> 请与 bingdi-b 做一次状态互检：先说明 A 在线，"
        "再请 B 给出结论；按实验室预算进行，不要无限循环。"
    )
    print("channel", args.channel, "target", args.target_user, "budgets", budget_a + "/" + budget_b)
    if args.dry_run:
        print("dry-run prompt:", prompt)
        return 0

    before = counts(dsn)
    ts = slack_post(token, args.channel, prompt)
    root_ts = ts
    print("driver_ts", ts, "attempts", args.attempts, "before", before)
    last = dict(before)
    deadline = time.time() + args.max_wait

    def wait_for_a_delta(previous, timeout=30):
        end = time.time() + timeout
        while time.time() < end:
            time.sleep(2)
            now = counts(dsn)
            if now["bots-lab-a"] > previous:
                print("inbox", now, "delta", {t: now[t] - before[t] for t in now})
                return now
        now = counts(dsn)
        print("inbox", now, "delta", {t: now[t] - before[t] for t in now}, "no_new_A")
        return now

    last = wait_for_a_delta(before["bots-lab-a"], timeout=35)
    for attempt in range(2, args.attempts + 1):
        if time.time() >= deadline:
            break
        text = "<@" + args.target_user + "> 第 " + str(attempt) + " 轮检查请求：请再次确认状态；只回复本轮。"
        reply_ts = slack_post(token, args.channel, text, thread_ts=root_ts)
        print("driver_ts", reply_ts, "attempt", attempt)
        last = wait_for_a_delta(last["bots-lab-a"], timeout=30)
    print("final", last, "delta", {t: last[t] - before[t] for t in last})
    print("expected_A_cap", budget_a, "attempted_A_messages", args.attempts)
    for line in deliveries(dsn):
        print("delivery", line)
    return 0


if __name__ == "__main__":
    sys.exit(main())
