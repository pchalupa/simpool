#!/usr/bin/env bash
# Multi-agent concurrency suite: several simpool processes hitting one pool at
# the same time, the way several agents on one Mac actually do.
#
# No agent and no model here -- these are correctness properties of the CLI, so
# they are fast, deterministic and safe to run in CI on every commit.
#
#   ./evals/race.sh              # every case
#   ./evals/race.sh double-lease # one case (substring match)
#
# Environment: RACE_AGENTS (parallel processes, default 8),
# SIMCTL_FAKE_LATENCY_MS (widen the race window, default 40).

set -uo pipefail

EVALS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$EVALS_DIR/lib/sandbox.sh"
set +e

FILTER="${1:-}"
AGENTS="${RACE_AGENTS:-8}"
export SIMCTL_FAKE_LATENCY_MS="${SIMCTL_FAKE_LATENCY_MS:-40}"

STAMP="$(date +%Y%m%d-%H%M%S)"
ROOT="$EVALS_DIR/runs/race-$STAMP"
PASSED=0
FAILED=0

pass() { echo "   PASS $1"; PASSED=$((PASSED + 1)); }
fail() { echo "   FAIL $1"; echo "        $2"; FAILED=$((FAILED + 1)); }

assert_eq() { # <expected> <actual> <what>
  if [[ "$1" == "$2" ]]; then pass "$3"; else fail "$3" "expected $1, got $2"; fi
}

# fresh <case> — a private pool, device host and database for one case.
fresh() {
  sandbox_build "$ROOT/$1" >/dev/null || exit 1
  SP="$SIMPOOL_REAL_BIN"
}

# fan_out <n> <dir> <args...> — run n commands at once, one output file each.
# Separate files matter: pretty-printed JSON appended concurrently to one file
# interleaves into garbage, which would look like a simpool bug and is not one.
fan_out() {
  local count="$1" dir="$2"; shift 2
  rm -rf "$dir"; mkdir -p "$dir"
  for index in $(seq 1 "$count"); do
    ( SIMPOOL_BORROWER="agent-$index" "$SP" "$@" >"$dir/$index.json" 2>"$dir/$index.err" ) &
  done
  wait
}

# results <dir> — every JSON document the fan-out produced, as a jq stream.
# A process that died before it could print JSON (a locked database, say)
# contributes a synthetic document so it is still counted.
results() {
  python3 - "$1" <<'PY'
import glob, json, os, sys
for path in sorted(glob.glob(os.path.join(sys.argv[1], "*.json"))):
    body = open(path).read().strip()
    try:
        print(json.dumps(json.loads(body)))
    except ValueError:
        stderr = open(path[:-5] + ".err").read().strip() if os.path.exists(path[:-5] + ".err") else ""
        print(json.dumps({"ok": False, "error": {"code": "no_json_output", "message": stderr or body}}))
PY
}

want() { [[ -z "$FILTER" || "$1" == *"$FILTER"* ]]; }

echo "simpool concurrency suite — $AGENTS parallel agents, ${SIMCTL_FAKE_LATENCY_MS}ms simctl latency"
echo

if want "double-lease"; then
  echo "── double-lease: $AGENTS agents race for a pool capped at 3 devices"
  fresh double-lease
  export SIMPOOL_MAX_DEVICES=3
  fan_out "$AGENTS" "$ROOT/double-lease/out" acquire --create-if-needed
  results "$ROOT/double-lease/out" >"$ROOT/double-lease/all.jsonl"

  granted=$(jq -s '[.[] | select(.ok)] | length' "$ROOT/double-lease/all.jsonl")
  devices=$(jq -s '[.[] | select(.ok) | .data.device.id] | unique | length' "$ROOT/double-lease/all.jsonl")
  codes=$(jq -rs '[.[] | select(.ok | not) | .error.code] | unique | sort | join(",")' "$ROOT/double-lease/all.jsonl")

  assert_eq "$granted" "$devices" "every granted lease landed on a different device"
  if (( granted >= 1 && granted <= 3 )); then
    pass "grants stayed within the device cap ($granted of max 3)"
  else
    fail "grants stayed within the device cap" "granted $granted"
  fi
  # Every loser must come back as parseable JSON carrying a code the skill
  # teaches the agent to act on. Anything else leaves an agent with nothing to
  # branch on.
  undocumented=$(jq -rs '[.[] | select(.ok | not) | .error.code | select(. != "no_capacity" and . != "no_device_available")] | unique | join(",")' "$ROOT/double-lease/all.jsonl")
  if [[ -z "$undocumented" ]]; then
    pass "losers got a documented code (${codes:-none})"
  else
    fail "losers got a documented code" "also saw: $undocumented"
  fi
  unset SIMPOOL_MAX_DEVICES
  echo
fi

if want "metro-port"; then
  echo "── metro-port: $AGENTS agents all ask for port 8081"
  fresh metro-port
  export SIMPOOL_MAX_DEVICES="$AGENTS"
  fan_out "$AGENTS" "$ROOT/metro-port/out" acquire --create-if-needed --meta metro-port=8081
  results "$ROOT/metro-port/out" >"$ROOT/metro-port/all.jsonl"

  holders=$(jq -s '[.[] | select(.ok) | select(.data.lease.metadata.metro_port == "8081")] | length' "$ROOT/metro-port/all.jsonl")
  conflicts=$(jq -s '[.[] | select(.ok | not) | select(.error.code == "metro_port_conflict")] | length' "$ROOT/metro-port/all.jsonl")

  assert_eq "1" "$holders" "exactly one agent holds port 8081"
  if (( conflicts >= 1 )); then
    pass "the rest were told metro_port_conflict ($conflicts)"
  else
    fail "the rest were told metro_port_conflict" "none seen"
  fi
  unset SIMPOOL_MAX_DEVICES
  echo
fi

if want "expiry"; then
  echo "── expiry: a lapsed lease frees the device and the old ID goes inert"
  fresh expiry
  export SIMPOOL_MAX_DEVICES=1
  old=$(SIMPOOL_LEASE_TTL=1s SIMPOOL_BORROWER=agent-gone "$SP" acquire --create-if-needed | jq -r '.data.lease.id')
  sleep 2
  second=$(SIMPOOL_BORROWER=agent-next "$SP" acquire)
  assert_eq "true" "$(echo "$second" | jq -r '.ok')" "the next agent got the device once the lease lapsed"

  renewed=$("$SP" renew "$old")
  assert_eq "lease_not_live" "$(echo "$renewed" | jq -r '.error.code')" "the lapsed lease ID cannot be renewed"

  released=$("$SP" release "$old")
  assert_eq "false" "$(echo "$released" | jq -r '.data.released')" "releasing with the lapsed ID does not touch the new borrower's lease"
  assert_eq "$(echo "$second" | jq -r '.data.lease.id')" "$("$SP" list | jq -r '.data[0].lease.id')" "the live lease still belongs to the new borrower"
  unset SIMPOOL_MAX_DEVICES
  echo
fi

if want "double-release"; then
  echo "── double-release: two processes release the same lease at once"
  fresh double-release
  lease=$("$SP" acquire --create-if-needed | jq -r '.data.lease.id')
  ( "$SP" release "$lease" >"$ROOT/double-release/a.json" 2>&1 ) &
  ( "$SP" release "$lease" >"$ROOT/double-release/b.json" 2>&1 ) &
  wait
  oks=$(jq -s '[.[] | select(.ok)] | length' "$ROOT/double-release/a.json" "$ROOT/double-release/b.json")
  assert_eq "2" "$oks" "releasing twice is safe, not an error"
  assert_eq "null" "$("$SP" list | jq -r '.data[0].lease')" "the device ended up free"
  echo
fi

if want "vanished-device"; then
  echo "── vanished-device: a simulator deleted outside simpool is not handed out"
  fresh vanished-device
  device=$("$SP" acquire --create-if-needed | jq -r '.data.device.id')
  lease=$("$SP" list | jq -r '.data[0].lease.id')
  xcrun simctl delete "$device" >/dev/null 2>&1

  after=$("$SP" list)
  assert_eq "true" "$(echo "$after" | jq -r --arg d "$device" '[.data[] | select(.device.id == $d) | .device.missing_at != null] | first // false')" "the device is marked missing"
  assert_eq "null" "$(echo "$after" | jq -r --arg d "$device" '[.data[] | select(.device.id == $d) | .lease] | first')" "its lease was closed"
  assert_eq "lease_not_live" "$("$SP" renew "$lease" | jq -r '.error.code')" "the borrower's lease ID is inert"
  echo
fi

if want "external-device"; then
  echo "── external-device: a simulator the user made is never handed out or deleted"
  fresh external-device
  export SIMPOOL_MAX_DEVICES=0
  personal=$(xcrun simctl create "Petr iPhone" com.apple.CoreSimulator.SimDeviceType.iPhone-17 com.apple.CoreSimulator.SimRuntime.iOS-27-0)

  assert_eq "false" "$("$SP" acquire --create-if-needed | jq -r '.ok')" "acquire refuses to borrow it"
  assert_eq "device_unmanaged" "$("$SP" delete "$personal" --force | jq -r '.error.code')" "delete --force still refuses to remove it"
  assert_eq "true" "$(xcrun simctl list -j devices | jq -r --arg d "$personal" '[.devices[][] | select(.udid == $d)] | length > 0')" "the user's simulator still exists"
  unset SIMPOOL_MAX_DEVICES
  echo
fi

echo "concurrency suite: $PASSED passed, $FAILED failed   (artifacts: $ROOT)"
[[ "$FAILED" -eq 0 ]]
