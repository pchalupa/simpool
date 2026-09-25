#!/usr/bin/env bash
# Run the agent-behaviour evaluation: for each scenario, put a fresh sandbox in
# a known state, let a real agent work in it, then score what it did to the pool.
#
#   ./evals/run.sh                        # every scenario, skill installed
#   ./evals/run.sh --scenario 02          # one scenario (prefix match)
#   ./evals/run.sh --arm no-skill         # ablation: how the CLI fares unguided
#   ./evals/run.sh --arm both --judge     # both arms, plus the LLM judge
#   ./evals/run.sh --repeat 3             # three runs per scenario, for variance
#
# Environment: EVAL_MODEL (default sonnet), EVAL_TIMEOUT (seconds, default 600),
# EVAL_JUDGE_MODEL (default opus), AGENT_CMD (drive a different agent).

set -euo pipefail

EVALS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$EVALS_DIR/.." && pwd)"
source "$EVALS_DIR/lib/sandbox.sh"

FILTER=""
ARMS="with-skill"
JUDGE=0
REPEAT=1

while [[ $# -gt 0 ]]; do
  case "$1" in
    --scenario) FILTER="$2"; shift 2 ;;
    --arm)
      case "$2" in
        both) ARMS="with-skill no-skill" ;;
        *) ARMS="$2" ;;
      esac
      shift 2 ;;
    --judge) JUDGE=1; shift ;;
    --repeat) REPEAT="$2"; shift 2 ;;
    -h|--help) sed -n '2,15p' "$0"; exit 0 ;;
    *) echo "unknown option $1" >&2; exit 2 ;;
  esac
done

command -v claude >/dev/null || { echo "claude CLI not found on PATH" >&2; exit 1; }

STAMP="$(date +%Y%m%d-%H%M%S)"
RESULTS="$EVALS_DIR/runs/$STAMP"
mkdir -p "$RESULTS"

echo "simpool agent evaluation — $STAMP"
echo "model: ${EVAL_MODEL:-sonnet}   arms: $ARMS   repeats: $REPEAT"
echo

( cd "$REPO_DIR" && make build >/dev/null )

for scenario in "$EVALS_DIR"/scenarios/*.json; do
  name="$(basename "$scenario" .json)"
  [[ -n "$FILTER" && "$name" != *"$FILTER"* ]] && continue

  for arm in $ARMS; do
    for attempt in $(seq 1 "$REPEAT"); do
      run_dir="$RESULTS/$name/$arm/$attempt"
      mkdir -p "$run_dir"

      echo "── $name [$arm] run $attempt"

      # 1. A sandbox: fake simulator host, private database, logging shim.
      #    It lives in the temp directory so no CLAUDE.md above the repository
      #    can reach the agent.
      sandbox_build >/dev/null
      sandbox_install_skill "$arm"
      export XCRUN_LOG="$SANDBOX/logs/xcrun-direct.jsonl"
      export WORK_LOG="$SANDBOX/logs/work.log"

      # 2. The scenario's own configuration and its fixture files.
      eval "$(python3 "$EVALS_DIR/lib/prepare.py" "$scenario")"

      # 3. Whatever other agents already did to this pool.
      sandbox_seed "$scenario" >/dev/null

      # 4. The prompt, with any seeded lease or device id substituted in.
      python3 "$EVALS_DIR/lib/prompt.py" "$scenario" >"$run_dir/prompt.txt"

      # 5. The agent works, sandboxed, with a wall-clock budget.
      python3 "$EVALS_DIR/lib/agent.py" "$run_dir/prompt.txt" "$SANDBOX/work" "$run_dir" >/dev/null || true

      # 6. Take the evidence out of the sandbox, then score what it did -- and,
      #    with --judge, what it told the user.
      sandbox_collect "$run_dir"
      python3 "$EVALS_DIR/lib/check.py" "$scenario" "$run_dir" >"$run_dir/result.json"
      if [[ "$JUDGE" == "1" ]]; then
        python3 "$EVALS_DIR/lib/judge.py" "$scenario" "$run_dir" >/dev/null || true
      fi

      python3 - "$run_dir/result.json" <<'PY'
import json, sys
report = json.load(open(sys.argv[1]))
for check in report["checks"]:
    print("   %s %s — %s" % ("PASS" if check["pass"] else "FAIL", check["id"], check["text"]))
    if not check["pass"]:
        print("        %s" % check["detail"])
print("   score %.0f%%  (%d simpool calls)" % (report["score"] * 100, report["metrics"]["simpool_calls"]))
PY
      echo
    done
  done
done

python3 "$EVALS_DIR/lib/report.py" "$RESULTS" | tee "$RESULTS/summary.md"
echo
echo "artifacts: $RESULTS"
