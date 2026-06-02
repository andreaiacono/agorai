#!/usr/bin/env bash
# Claude Code hook -> agorai. Forwards the hook's JSON (stdin) plus the
# AGORAI_ID env var (which agorai set when it spawned this claude) to the
# running agorai server. Fire-and-forget; never blocks claude.
#
# --noproxy: never route the localhost call through an HTTP(S) proxy.
# -4: force IPv4 (the server listens on 127.0.0.1). --connect-timeout/-m: fail fast.
exec curl -s --noproxy '*' -4 --connect-timeout 1 -m 2 \
  -X POST "http://127.0.0.1:7777/hook" \
  -H "X-Agorai-Id: ${AGORAI_ID:-external}" \
  --data-binary @- >/dev/null 2>&1
