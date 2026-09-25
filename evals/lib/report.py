#!/usr/bin/env python3
"""Aggregate a results directory into a Markdown scorecard.

The table that matters is scenario x arm: the with-skill column says how well
the CLI is driven when the shipped skill is loaded, the no-skill column says how
much of that came from the skill rather than from the CLI's own affordances.

Usage: report.py <results-dir>
"""

import collections
import json
import os
import sys


def collect(results_dir):
    runs = []
    for scenario in sorted(os.listdir(results_dir)):
        scenario_dir = os.path.join(results_dir, scenario)
        if not os.path.isdir(scenario_dir):
            continue
        for arm in sorted(os.listdir(scenario_dir)):
            for attempt in sorted(os.listdir(os.path.join(scenario_dir, arm))):
                run_dir = os.path.join(scenario_dir, arm, attempt)
                result_path = os.path.join(run_dir, "result.json")
                if not os.path.exists(result_path):
                    continue
                result = json.load(open(result_path))
                judge_path = os.path.join(run_dir, "judge.json")
                result["judge"] = json.load(open(judge_path)) if os.path.exists(judge_path) else None
                agent_path = os.path.join(run_dir, "agent.json")
                result["agent"] = json.load(open(agent_path)) if os.path.exists(agent_path) else {}
                runs.append((scenario, arm, result))
    return runs


def mean(values):
    return sum(values) / len(values) if values else 0.0


def main(results_dir):
    runs = collect(results_dir)
    if not runs:
        print("no results in %s" % results_dir)
        return

    by_key = collections.defaultdict(list)
    for scenario, arm, result in runs:
        by_key[(scenario, arm)].append(result)

    arms = sorted({arm for _, arm, _ in runs})
    scenarios = sorted({scenario for scenario, _, _ in runs})

    out = ["# simpool agent evaluation", "", "## Scores", ""]
    out.append("| scenario | " + " | ".join(arms) + " | simpool calls | judge |")
    out.append("|---|" + "---|" * (len(arms) + 2))

    for scenario in scenarios:
        cells = []
        for arm in arms:
            results = by_key.get((scenario, arm), [])
            cells.append("%.0f%%" % (mean([r["score"] for r in results]) * 100) if results else "—")
        scenario_runs = [r for s, _, r in runs if s == scenario]
        calls = mean([r["metrics"]["simpool_calls"] for r in scenario_runs] or [0])
        verdicts = [
            verdict
            for r in scenario_runs
            if r.get("judge")
            for verdict in r["judge"].get("verdicts", [])
        ]
        judged = (
            "%d/%d" % (sum(1 for v in verdicts if v.get("pass")), len(verdicts)) if verdicts else "—"
        )
        out.append("| %s | %s | %.1f | %s |" % (scenario, " | ".join(cells), calls, judged))

    out += ["", "## Failing checks", ""]
    failures = collections.Counter()
    details = collections.defaultdict(list)
    for scenario, arm, result in runs:
        for check in result["checks"]:
            if not check["pass"]:
                failures[(scenario, arm, check["id"], check["text"])] += 1
                details[(scenario, arm, check["id"])].append(check["detail"])

    if not failures:
        out.append("None — every check passed in every run.")
    for (scenario, arm, check_id, text), count in failures.most_common():
        out.append("- **%s** [%s] %s — %s (%dx)" % (scenario, arm, check_id, text, count))
        out.append("  - %s" % details[(scenario, arm, check_id)][0])

    out += ["", "## Cost", ""]
    for arm in arms:
        results = [r for _, a, r in runs if a == arm]
        out.append(
            "- **%s**: %.0f%% mean score, %.1f simpool calls/run, %.0fs mean wall clock, $%.3f mean cost"
            % (
                arm,
                mean([r["score"] for r in results]) * 100,
                mean([r["metrics"]["simpool_calls"] for r in results]),
                mean([r["agent"].get("wall_seconds", 0) for r in results]),
                mean([r["agent"].get("cost_usd") or 0 for r in results]),
            )
        )

    print("\n".join(out))


if __name__ == "__main__":
    main(sys.argv[1])
