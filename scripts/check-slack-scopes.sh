#!/usr/bin/env sh
# Verify the installed Slack bot token has only the scopes this MVP uses.
# Required: app_mentions:read, chat:write, and channel history for the
# configured channel type. Run with SLACK_BOT_TOKEN exported; never echo it.
set -eu

: "${SLACK_BOT_TOKEN:?export SLACK_BOT_TOKEN before running this check}"
command -v curl >/dev/null 2>&1 || { echo "curl is required" >&2; exit 2; }

headers=$(mktemp "${TMPDIR:-/tmp}/bots-slack-scopes.XXXXXX")
trap 'rm -f "$headers"' EXIT HUP INT TERM

if ! curl -sS -D "$headers" -o /dev/null -H "Authorization: Bearer $SLACK_BOT_TOKEN" https://slack.com/api/auth.test; then
  echo "auth.test request failed" >&2
  exit 2
fi

actual=$(grep -i '^x-oauth-scopes:' "$headers" | sed 's/^[^:]*: //' | tr -d '\r' | tr ',' '\n' | sed '/^[[:space:]]*$/d' | sort -u)
if [ -z "$actual" ]; then
  echo "no x-oauth-scopes header; check auth.test response" >&2
  exit 2
fi

actual_file=$(mktemp "${TMPDIR:-/tmp}/bots-slack-actual.XXXXXX")
allowed_file=$(mktemp "${TMPDIR:-/tmp}/bots-slack-allowed.XXXXXX")
trap 'rm -f "$headers" "$actual_file" "$allowed_file"' EXIT HUP INT TERM
printf '%s\n' "$actual" | sort -u > "$actual_file"
printf '%s\n' app_mentions:read chat:write channels:history groups:history | sort -u > "$allowed_file"

unexpected=$(comm -23 "$actual_file" "$allowed_file")
if [ -n "$unexpected" ]; then
  echo "unexpected Slack scopes (remove from the app and reinstall):" >&2
  printf '  %s\n' $unexpected >&2
  exit 1
fi

echo "Slack scopes are within the MVP allowlist:"
printf '  %s\n' $actual
