#!/usr/bin/env python3
"""Run one agent against one scenario prompt, inside an already-built sandbox.

Claude Code is the default driver. Any other agent can be evaluated by setting
AGENT_CMD to a shell command: it is run with the prompt in $EVAL_PROMPT_FILE and
the same sandboxed PATH, so the scenarios and the scoring stay the same.

Usage: agent.py <prompt-file> <work-dir> <out-dir>
"""

import json
import os
import subprocess
import sys
import time

TIMEOUT = int(os.environ.get("EVAL_TIMEOUT", "600"))
MODEL = os.environ.get("EVAL_MODEL", "sonnet")


def claude_argv(prompt):
    return [
        "claude",
        "-p",
        prompt,
        "--output-format",
        "json",
        "--model",
        MODEL,
        # The sandbox is the point: let the agent run commands without prompts,
        # but cut it off from the host's MCP servers and the network.
        "--permission-mode",
        "bypassPermissions",
        "--allow-dangerously-skip-permissions",
        "--strict-mcp-config",
        "--mcp-config",
        '{"mcpServers":{}}',
        "--disallowedTools",
        "WebSearch WebFetch",
    ]


def main(prompt_path, work_dir, out_dir):
    prompt = open(prompt_path).read()
    env = dict(os.environ, EVAL_PROMPT_FILE=prompt_path)

    started = time.time()
    if env.get("AGENT_CMD"):
        proc = run(["bash", "-lc", env["AGENT_CMD"]], work_dir, env, prompt)
    else:
        proc = run(claude_argv(prompt), work_dir, env, None)
    elapsed = round(time.time() - started, 1)

    raw = proc.stdout if proc else ""
    summary = {"timed_out": proc is None, "wall_seconds": elapsed, "final_text": raw.strip()}

    try:
        payload = json.loads(raw)
        summary["final_text"] = payload.get("result", "")
        summary["turns"] = payload.get("num_turns")
        summary["cost_usd"] = payload.get("total_cost_usd")
        summary["agent_error"] = payload.get("is_error")
    except ValueError:
        pass

    with open(os.path.join(out_dir, "agent.json"), "w") as handle:
        json.dump(summary, handle, indent=2)
    with open(os.path.join(out_dir, "agent-stdout.txt"), "w") as handle:
        handle.write(raw)
    if proc is not None and proc.stderr:
        with open(os.path.join(out_dir, "agent-stderr.txt"), "w") as handle:
            handle.write(proc.stderr)

    print(summary["final_text"][:400])


def run(argv, cwd, env, stdin_text):
    try:
        return subprocess.run(
            argv,
            cwd=cwd,
            env=env,
            input=stdin_text,
            capture_output=True,
            text=True,
            timeout=TIMEOUT,
        )
    except subprocess.TimeoutExpired:
        sys.stderr.write("agent timed out after %ss\n" % TIMEOUT)
        return None


if __name__ == "__main__":
    main(sys.argv[1], sys.argv[2], sys.argv[3])
