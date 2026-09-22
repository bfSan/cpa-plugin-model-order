#!/usr/bin/env bash
# Browser smoke test for the ordering panel.
#
# The Go tests render the panel HTML but never execute it, so a JavaScript syntax
# error still ships green. That is not hypothetical: the management base path was
# once injected inside its own quotes, producing `= ""/v0/management""`, which
# killed the whole script block and left every operator looking at an empty page.
# This check runs the page in a real browser and asserts the script executed.
#
# Usage: ./scripts/panel-smoke.sh [panel-url]
set -uo pipefail

URL="${1:-http://192.168.110.185:8317/v0/resource/plugins/model-order/panel}"
CHROME="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
[ -x "$CHROME" ] || CHROME="$(command -v chromium || command -v google-chrome || true)"
[ -n "$CHROME" ] || { echo "SKIP: no chrome/chromium binary (set CHROME=)"; exit 0; }

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
curl -s --max-time 15 "$URL" -o "$tmp/panel.html" || { echo "FAIL: cannot fetch $URL"; exit 1; }

status=0

# 1. The host injects a quoted literal, so the assignment must read exactly once.
if grep -qE '^const MANAGEMENT_BASE_PATH = "[^"]*";$' "$tmp/panel.html"; then
  echo "ok   MANAGEMENT_BASE_PATH is a single valid JS literal"
else
  echo "FAIL MANAGEMENT_BASE_PATH is not a valid JS literal (double quoting blanks the panel)"
  grep -n "MANAGEMENT_BASE_PATH =" "$tmp/panel.html" | head -2
  status=1
fi

# 2. The placeholder must be gone, or the same line would be a syntax error.
if grep -q "__MO_MANAGEMENT_BASE_PATH_JSON__" "$tmp/panel.html"; then
  echo "FAIL placeholder was never substituted"; status=1
else
  echo "ok   placeholder substituted by the host"
fi

# 3. Run it. boot() writes the API path into the footer, so a populated footer is
#    proof the script parsed and executed, independent of whether a key exists.
"$CHROME" --headless=new --disable-gpu --no-first-run --virtual-time-budget=5000 \
  --dump-dom "$URL" >"$tmp/dom.html" 2>/dev/null
if grep -qE '<code id="apiPath">/[^<]+</code>' "$tmp/dom.html"; then
  echo "ok   panel script executed in a real browser"
else
  echo "FAIL panel script did not run: the page renders empty"
  status=1
fi

exit $status
