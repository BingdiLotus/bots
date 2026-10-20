#!/usr/bin/env python3
"""Generate one G2 SOP candidate from the real source trace with the real model.

The proposal context is source.json + baseline.json only. It deliberately does
not read suite.json, checker.py or frozen.json's case expectations. The result
is a candidate, not a published configuration; a human decision is separate.
"""
import argparse
import datetime as dt
import hashlib
import json
import os
import sys
import time

import requests

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))


def stable(prefix, *parts):
    return prefix + hashlib.sha256(json.dumps(parts, ensure_ascii=False, separators=(",", ":")).encode()).hexdigest()[:32]


def request(method, url, token, key, body=None, timeout=30):
    headers = {"Authorization": "Bearer " + token, "Accept": "application/json"}
    if key:
        headers["Idempotency-Key"] = key
    if body is not None:
        headers["Content-Type"] = "application/json"
    response = requests.request(method, url, headers=headers, json=body, timeout=timeout)
    if response.status_code not in (200, 201, 202):
        raise RuntimeError(f"{method} {url.split('?')[0]} -> {response.status_code}: {response.text[:500]}")
    return response.json()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--api", default="http://127.0.0.1:8080")
    parser.add_argument("--model", default="claude-sonnet-4-6")
    parser.add_argument("--timeout", type=int, default=180)
    parser.add_argument("--nonce", default="", help="optional nonce for a new idempotency key; prompt is unchanged")
    args = parser.parse_args()
    token = os.getenv("BOTS_CHRONOTOPE_TOKEN")
    if not token:
        raise SystemExit("BOTS_CHRONOTOPE_TOKEN is required")
    source = json.load(open(os.path.join(ROOT, "test/sop/source.json")))
    baseline = json.load(open(os.path.join(ROOT, "test/sop/baseline.json")))
    baseline_instructions = baseline["config"]["instructions"]
    source_hash = hashlib.sha256(json.dumps([source["input"], source["final"], source["correction"], baseline_instructions], ensure_ascii=False, separators=(",", ":")).encode()).hexdigest()
    prompt = f"""你是一个组织 Bot 的治理助手。只输出一个 JSON 对象，不要输出 Markdown 或其它文字。

任务：根据下面的真实轨迹和人工纠正，为 baseline SOP 提出一条最小、可检验的补丁。
约束：
1. 只允许向 instructions 追加一条中文 SOP，不得改变模型、工具、权限、限额、知识或外部效果。
2. 补丁不超过 120 个汉字，必须针对轨迹中的具体行为，不要泛泛而谈。
3. hypothesis 必须给出可观察的预期变化，以及不能回退的 guard（正确性、安全或工具行为）。
4. 不得要求隐藏工具结果、伪造来源、改变发送或跳过询问。

真实来源：
- 成员输入：{source["input"]}
- 实际 final：{source["final"]}
- 观察到的问题：{source["observed_problem"]}
- 人工纠正：{source["correction"]}

Baseline instructions：
{baseline_instructions}

输出 JSON 格式：
{{"patch":"追加的一条中文 SOP","hypothesis":"可检验假设与 guard"}}
"""
    key = stable("sop_proposal_", source_hash, args.model, args.nonce)
    agent = request("POST", f"{args.api}/orgs/{baseline['tenant_id']}/agents", token, key,
                    {"name": "SOP proposal / " + source["run_id"], "config": {
                        "model": args.model, "version": 1,
                        "instructions": "你是严格的治理助手。只输出用户要求的 JSON 对象。",
                        "application_profile": False, "max_output_tokens": 1200, "max_steps": 1, "max_duration_ms": 120000}})
    session = request("POST", f"{args.api}/agents/{agent['id']}/sessions", token, stable("sop_proposal_session_", key),
                      {"subject_ref": "sop-proposal", "scope_id": "sop-proposal"})
    run = request("POST", f"{args.api}/sessions/{session['id']}/runs", token, stable("sop_proposal_run_", key),
                  {"input": prompt, "origin_command_id": stable("sop_proposal_command_", key)})
    run_id = run["run_id"]
    deadline = time.monotonic() + args.timeout
    while time.monotonic() < deadline:
        result = request("GET", f"{args.api}/runs/{run_id}", token, "")
        if result["status"] in ("completed", "failed", "canceled"):
            break
        time.sleep(2)
    else:
        raise SystemExit("proposal run did not finish in time: " + run_id)
    if result["status"] != "completed" or not result.get("final"):
        raise SystemExit(f"proposal run ended {result['status']}: {result.get('error')}")
    raw = result["final"].strip()
    if raw.startswith("```"):
        raw = raw.strip("`")
        if raw.startswith("json"):
            raw = raw[4:]
    payload = json.loads(raw)
    patch = (payload.get("patch") or "").strip()
    hypothesis = (payload.get("hypothesis") or "").strip()
    if not patch or not hypothesis or len(patch) > 240:
        raise SystemExit("proposal JSON is incomplete or patch is too long")
    candidate_instructions = baseline_instructions.rstrip() + "\n\n" + patch
    candidate = {
        "version": 1,
        "generated_at": dt.datetime.now().astimezone().isoformat(timespec="seconds"),
        "proposal_run_id": run_id,
        "proposal_agent_id": agent["id"],
        "proposal_session_id": session["id"],
        "proposal_model": args.model,
        "proposal_nonce": args.nonce,
        "proposal_usage": result.get("usage"),
        "proposal_prompt_sha256": hashlib.sha256(prompt.encode()).hexdigest(),
        "source_trace_sha256": hashlib.sha256(json.dumps(source, ensure_ascii=False, sort_keys=True).encode()).hexdigest(),
        "baseline_revision": baseline["revision"],
        "baseline_config_hash": baseline["config_hash"],
        "baseline_runtime_agent_id": baseline["runtime_agent_id"],
        "baseline_instructions": baseline_instructions,
        "patch": patch,
        "hypothesis": hypothesis,
        "candidate_instructions": candidate_instructions,
        "candidate_instructions_sha256": hashlib.sha256(candidate_instructions.encode()).hexdigest(),
        "frozen_suite_used_by_proposal": False,
        "decision": None,
        "publish": None
    }
    path = os.path.join(ROOT, "test/sop/candidate.json")
    json.dump(candidate, open(path, "w"), ensure_ascii=False, indent=2)
    print(json.dumps({key: candidate[key] for key in ("proposal_run_id", "patch", "hypothesis", "proposal_usage")}, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
