#!/usr/bin/env bash
# Claude Code statusLine -> agorai. Claude pipes a JSON session-status payload on
# stdin (model, context_window, and — for Pro/Max accounts after the first API
# response — rate_limits). We forward it to the running agorai server so the
# dashboard can show account usage limits next to context fill, then render a
# status line (the user's own if they had one, else a compact default).
#
# Env set by agorai when it spawned this claude:
#   AGORAI_ID              correlation id (== the dashboard session id)
#   AGORAI_USER_STATUSLINE the user's pre-existing statusLine command, if any
#
# --noproxy/-4: keep the localhost call off any proxy and on IPv4.
# Tight timeouts + background: never block claude's render on the network.
input=$(cat)

printf '%s' "$input" | curl -s --noproxy '*' -4 --connect-timeout 1 -m 2 \
  -X POST "http://127.0.0.1:7777/api/usage?id=${AGORAI_ID:-external}" \
  -H 'Content-Type: application/json' --data-binary @- >/dev/null 2>&1 &

# Display: re-run the user's own statusline so their terminal looks unchanged.
if [ -n "$AGORAI_USER_STATUSLINE" ]; then
  printf '%s' "$input" | sh -c "$AGORAI_USER_STATUSLINE"
  exit 0
fi

# No user statusline configured — print a compact default (best-effort; needs
# jq, stays silent if it's not installed).
command -v jq >/dev/null 2>&1 || exit 0
printf '%s' "$input" | jq -r '
  "[\(.model.display_name)]  ctx \(.context_window.used_percentage // 0 | floor)%"
  + (if .rate_limits.five_hour.used_percentage != null
       then "  ·  5h \(.rate_limits.five_hour.used_percentage | floor)%" else "" end)
  + (if .rate_limits.seven_day.used_percentage != null
       then "  ·  7d \(.rate_limits.seven_day.used_percentage | floor)%" else "" end)
'
