#!/usr/bin/env bash
# Sandbox construction for simpool evaluations. Sourced by run.sh and race.sh.
#
# A sandbox is one throwaway directory holding a simulator host (the fake
# xcrun), a simpool database, a command log, and a working directory the agent
# is pointed at. Nothing outside it is touched: no real simulator, no
# ~/Library/Application Support/simpool.

set -euo pipefail

EVALS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO_DIR="$(cd "$EVALS_DIR/.." && pwd)"

# sandbox_build [dir] — create the sandbox and export the environment that both
# the agent and any direct simpool call in the harness must run under.
#
# The sandbox lives in the system temp directory, never under the repository:
# Claude Code walks parent directories for CLAUDE.md and .claude/rules, so a
# sandbox inside a checkout would hand the agent instructions the scenario never
# wrote. Evidence is copied back out by sandbox_collect when the run ends.
sandbox_build() {
  local root="${1:-$(mktemp -d "${TMPDIR:-/tmp}/simpool-eval.XXXXXX")}"

  rm -rf "$root"
  mkdir -p "$root/bin" "$root/state" "$root/work" "$root/logs"

  cp "$EVALS_DIR/lib/xcrun" "$root/bin/xcrun"
  cp "$EVALS_DIR/lib/simpool-wrapper" "$root/bin/simpool"
  chmod +x "$root/bin/xcrun" "$root/bin/simpool"

  export SANDBOX="$root"
  export SIMPOOL_REAL_BIN="${SIMPOOL_BIN:-$REPO_DIR/bin/simpool}"
  export SIMPOOL_STATE_DIR="$root/state"
  export SIMCTL_STATE="$root/state/simctl.json"
  export SIMPOOL_EVAL_LOG="$root/logs/simpool-commands.jsonl"
  export PATH="$root/bin:$PATH"

  : >"$SIMPOOL_EVAL_LOG"

  if [[ ! -x "$SIMPOOL_REAL_BIN" ]]; then
    echo "simpool binary not found at $SIMPOOL_REAL_BIN — run 'make build'" >&2
    return 1
  fi
}

# sandbox_install_skill <mode> — put the shipped skill in the agent's project so
# Claude Code discovers it, or leave the project bare for the ablation arm.
sandbox_install_skill() {
  local mode="$1"

  if [[ "$mode" == "with-skill" ]]; then
    mkdir -p "$SANDBOX/work/.claude/skills"
    cp -R "$REPO_DIR/skills/simpool" "$SANDBOX/work/.claude/skills/simpool"
  fi

  # Never inherit the host's settings, MCP servers or CLAUDE.md into a run.
  mkdir -p "$SANDBOX/work/.claude"
  cat >"$SANDBOX/work/.claude/settings.json" <<'JSON'
{
  "enableAllProjectMcpServers": false
}
JSON
}

# sandbox_seed <scenario.json> — replay the scenario's `seed` commands as other
# agents would have left the pool: leases held by someone else, devices already
# created, ports already claimed.
sandbox_seed() {
  local scenario="$1"
  python3 "$EVALS_DIR/lib/seed.py" "$scenario"
}

# sandbox_collect <run-dir> — copy the evidence out of the sandbox so the scoring
# runs against artifacts that outlive it, then throw the sandbox away.
sandbox_collect() {
  local run_dir="$1"

  mkdir -p "$run_dir"
  cp -R "$SANDBOX/logs" "$run_dir/logs"
  cp -R "$SANDBOX/state" "$run_dir/state"
  # The fake simulator host travels with the evidence: scoring runs `simpool
  # list` again, and it must see the sandbox's devices, not the real Mac's.
  cp -R "$SANDBOX/bin" "$run_dir/bin"

  if [[ "${EVAL_KEEP_SANDBOX:-0}" == "1" ]]; then
    echo "   sandbox kept at $SANDBOX"
  else
    rm -rf "$SANDBOX"
  fi
}
