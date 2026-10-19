#!/usr/bin/env python3
"""Test-only scripted HTTP model and counting Slack sink. No Bots domain state.

Real Bots owns its PG inbox, tools, knowledge, tasks, fire and delivery records.
This fixture only records model requests and provider calls for gate assertions.
"""
import argparse
import datetime as dt
import json
import re
import sqlite3
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


def encoded(value):
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"))


def validate_tools(body):
    expected = {
        "remember": {"content": "string", "kind": "string"},
        "recall": {"query": "string"},
        "create_reminder": {"due_at": "string", "text": "string"},
        "update_reminder": {"task_id": "string", "expected_revision": "integer", "due_at": "string", "text": "string"},
        "cancel_reminder": {"task_id": "string", "expected_revision": "integer"},
    }
    tools = body.get("tools")
    if not isinstance(tools, list) or len(tools) != len(expected):
        raise ValueError("the model must receive exactly five native function tools")
    seen = set()
    for tool in tools:
        fn = tool.get("function", {})
        name = fn.get("name")
        if tool.get("type") != "function" or name not in expected or name in seen:
            raise ValueError("native function names must be complete and unique")
        seen.add(name)
        if not isinstance(fn.get("description"), str) or not fn["description"].strip():
            raise ValueError("each native tool must carry its actual description")
        schema = fn.get("parameters", {})
        properties, required = schema.get("properties", {}), schema.get("required", [])
        if (schema.get("type") != "object" or schema.get("additionalProperties") is not False
                or set(properties) != set(expected[name]) or len(required) != len(expected[name])
                or set(required) != set(expected[name])):
            raise ValueError("native schemas must contain exactly the required business inputs")
        for field, typ in expected[name].items():
            if properties[field].get("type") != typ:
                raise ValueError("native input types changed")
        if "due_at" in properties and properties["due_at"].get("format") != "date-time":
            raise ValueError("reminder due_at must use an absolute date-time schema")
        if "expected_revision" in properties and properties["expected_revision"].get("minimum") != 1:
            raise ValueError("reminder CAS revision must be positive")
        if "kind" in properties and properties["kind"].get("enum") != ["knowledge", "experience"]:
            raise ValueError("knowledge/experience kinds must match the native tool contract")
    if body.get("max_completion_tokens") != 1024:
        raise ValueError("the configured 1024-token generation limit must reach the HTTP provider")


class Fixture:
    def __init__(self, path):
        self.path = path
        self.lock = threading.Lock()
        with self.db() as db:
            db.executescript("""
              PRAGMA journal_mode=WAL;
              CREATE TABLE IF NOT EXISTS models(id INTEGER PRIMARY KEY,request TEXT,verified INTEGER);
              CREATE TABLE IF NOT EXISTS sends(id INTEGER PRIMARY KEY,payload TEXT,ts TEXT);
            """)

    def db(self):
        db = sqlite3.connect(self.path, timeout=10)
        db.row_factory = sqlite3.Row
        db.execute("PRAGMA synchronous=FULL")
        return db

    def model(self, body):
        validate_tools(body)
        messages = body.get("messages", [])
        user_index = max(i for i, m in enumerate(messages) if m.get("role") == "user")
        user = messages[user_index]["content"]
        current = messages[user_index + 1:]
        tool_messages = [m for m in current if m.get("role") == "tool"]
        outputs = [json.loads(m["content"]) for m in tool_messages]
        recall = "CASE_RECALL" in user
        calls, verified = [], False
        if not outputs:
            if recall:
                calls = [("recall", {"query": "G0 native knowledge"})]
            else:
                due = re.search(r"DUE=(\S+)", user).group(1)
                calls = [("remember", {"content": "G0 native knowledge: team review is Friday", "kind": "knowledge"}),
                         ("create_reminder", {"due_at": due, "text": "G0 native reminder: team review"})]
            delta = {"tool_calls": [{"index": i, "id": "g0_call_" + name, "type": "function",
                      "function": {"name": name, "arguments": encoded(args)}} for i, (name, args) in enumerate(calls)]}
        else:
            expected_order = ["g0_call_recall"] if recall else ["g0_call_remember", "g0_call_create_reminder"]
            expected_ids = set(expected_order)
            actual_ids = [m.get("tool_call_id") for m in tool_messages]
            assistant_calls = [c for m in current if m.get("role") == "assistant" for c in m.get("tool_calls", [])]
            assistant_ids = [c["id"] for c in assistant_calls]
            if (actual_ids != expected_order or assistant_ids != expected_order
                    or len(tool_messages) != len(expected_ids) or len(assistant_calls) != len(expected_ids)):
                raise ValueError("all real tool results and original call IDs must reach the next model request")
            for call in assistant_calls:
                fn = call.get("function", {})
                if call.get("type") != "function" or call["id"] != "g0_call_" + fn.get("name", ""):
                    raise ValueError("assistant call names must remain associated with their original IDs")
            by_id = {message["tool_call_id"]: output for message, output in zip(tool_messages, outputs)}
            if recall:
                items = by_id["g0_call_recall"].get("items", [])
                if len(items) != 1 or not items[0].get("source_id") or not items[0].get("origin_command_id"):
                    raise ValueError("new session recall did not receive persisted scoped knowledge and source")
                identity = items[0]["id"]
            else:
                identity = by_id["g0_call_remember"].get("knowledge_id")
                if not by_id["g0_call_create_reminder"].get("task_id"):
                    raise ValueError("remember and create_reminder must both execute")
            if not identity or not re.fullmatch(r"knowledge_[0-9a-f]{32}", identity):
                raise ValueError("missing actual unpredictable knowledge identity")
            verified = True
            delta = {"content": ("RECALL " if recall else "REMEMBER ") + identity}
        with self.lock, self.db() as db:
            db.execute("INSERT INTO models(request,verified) VALUES(?,?)", (encoded(body), int(verified)))
        return delta, "tool_calls" if calls else "stop"

    def send(self, body):
        if not body.get("client_msg_id") or not body.get("channel") or not body.get("thread_ts") or not body.get("text"):
            raise ValueError("sender did not provide trusted target and operation correlation")
        with self.lock, self.db() as db:
            number = db.execute("SELECT count(*) FROM sends").fetchone()[0] + 1
            timestamp = f"{int(time.time())}.{number:06d}"
            db.execute("INSERT INTO sends(payload,ts) VALUES(?,?)", (encoded(body), timestamp))
        return {"ok": True, "channel": body["channel"], "ts": timestamp}

    def state(self):
        with self.db() as db:
            return {"models": [dict(r) for r in db.execute("SELECT * FROM models ORDER BY id")],
                    "sends": [dict(r) for r in db.execute("SELECT * FROM sends ORDER BY id")]}


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *_):
        pass

    def reply(self, status, body):
        payload = encoded(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(payload)
        self.close_connection = True

    def do_GET(self):
        if self.path == "/healthz":
            return self.reply(200, {"test_fixture": True})
        if self.path == "/state":
            return self.reply(200, self.server.fixture.state())
        return self.reply(404, {})

    def do_POST(self):
        try:
            length = int(self.headers.get("Content-Length", "0"))
            if not 0 < length <= 1 << 20:
                return self.reply(400, {"error": "invalid length"})
            body = json.loads(self.rfile.read(length))
            if self.path == "/api/chat.postMessage":
                return self.reply(200, self.server.fixture.send(body))
            if self.path != "/v1/chat/completions":
                return self.reply(404, {})
            delta, reason = self.server.fixture.model(body)
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Connection", "close")
            self.end_headers()
            base = {"id": "bots-g0-model", "object": "chat.completion.chunk", "created": int(time.time()), "model": body["model"]}
            chunks = [dict(base, choices=[{"index": 0, "delta": delta, "finish_reason": None}]),
                      dict(base, choices=[{"index": 0, "delta": {}, "finish_reason": reason}]),
                      dict(base, choices=[], usage={"prompt_tokens": 10, "completion_tokens": 10, "total_tokens": 20})]
            for chunk in chunks:
                self.wfile.write(("data: " + encoded(chunk) + "\n\n").encode())
            self.wfile.write(b"data: [DONE]\n\n")
            self.wfile.flush()
            self.close_connection = True
        except (ValueError, KeyError, TypeError, StopIteration, AttributeError) as exc:
            self.reply(400, {"error": str(exc)})


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--port", type=int, default=18090)
    parser.add_argument("--db", required=True)
    args = parser.parse_args()
    server = ThreadingHTTPServer(("0.0.0.0", args.port), Handler)
    server.daemon_threads = True
    server.fixture = Fixture(args.db)
    server.serve_forever(poll_interval=0.2)
