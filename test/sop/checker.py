"""Independent G2 case checker.

This file and suite.json are frozen before candidate generation. The proposal
prompt must not include them. Checks read actual provider/delivery output and
native Bots state; they do not ask the model to score itself.
"""
from __future__ import annotations

import datetime as dt
import re


def _time(value):
    if isinstance(value, dt.datetime):
        return value
    return dt.datetime.fromisoformat(value.replace("Z", "+00:00"))


def _contains_any(text, alternatives):
    return any(option in text for option in alternatives)


def check_case(case, observed):
    expect = case["expect"]
    failures = []
    final = observed.get("final") or ""
    tools = observed.get("tools") or []

    for text in expect.get("final_contains", []):
        if text not in final:
            failures.append(f"final missing {text!r}")
    for text in expect.get("final_not_contains", []):
        if text in final:
            failures.append(f"final contains forbidden {text!r}")
    for alternatives in expect.get("final_contains_any", []):
        if not _contains_any(final, alternatives):
            failures.append(f"final missing any of {alternatives!r}")
    for pattern in expect.get("final_not_regex", []):
        if re.search(pattern, final):
            failures.append(f"final matches forbidden pattern {pattern!r}")

    for tool in expect.get("tools", []):
        if tool not in tools:
            failures.append(f"missing tool {tool!r}; saw {tools!r}")
    for tool in expect.get("forbid_tools", []):
        if tool in tools:
            failures.append(f"unexpected tool {tool!r}")

    if "knowledge_delta" in expect and observed.get("knowledge_delta") != expect["knowledge_delta"]:
        failures.append(f"knowledge_delta={observed.get('knowledge_delta')} want {expect['knowledge_delta']}")
    if "task_delta" in expect and observed.get("task_delta") != expect["task_delta"]:
        failures.append(f"task_delta={observed.get('task_delta')} want {expect['task_delta']}")

    if "task_due_offset_seconds" in expect:
        task = observed.get("task")
        if not task:
            failures.append("expected a native reminder task")
        else:
            accepted = _time(observed["accepted_at"])
            due = _time(task["due_at"])
            expected = expect["task_due_offset_seconds"]
            tolerance = expect.get("task_due_tolerance_seconds", 15)
            delta = (due - accepted).total_seconds()
            if abs(delta - expected) > tolerance:
                failures.append(f"task due offset={delta:.1f}s want {expected}s +/- {tolerance}s")
            if task.get("target", {}).get("channel_id") != observed.get("channel"):
                failures.append("task channel target changed")
            if task.get("target", {}).get("thread_id") != observed.get("thread"):
                failures.append("task thread target changed")
            text = expect.get("task_text_contains")
            if text and text not in (task.get("text") or ""):
                failures.append(f"task text missing {text!r}: {task.get('text')!r}")

    return failures
