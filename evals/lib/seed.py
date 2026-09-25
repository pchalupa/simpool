#!/usr/bin/env python3
"""Put the pool into the state a scenario starts from.

A scenario's `world.seed` list is replayed with the real binary (not the logging
shim) and a different borrower identity, so the agent's own command log stays
clean and the leases it meets look like another agent's. Anything a seed command
returns can be captured under a name and referenced from the prompt or the
checks as {{name.path.to.field}}.
"""

import json
import os
import subprocess
import sys
import time


def run_seed(scenario_path):
    with open(scenario_path) as handle:
        scenario = json.load(handle)

    world = scenario.get("world", {})
    facts = {}

    base_env = dict(os.environ, SIMPOOL_INTERNAL="1")
    base_env.update({key: str(value) for key, value in world.get("env", {}).items()})
    if "max_devices" in world:
        base_env["SIMPOOL_MAX_DEVICES"] = str(world["max_devices"])

    binary = os.environ["SIMPOOL_REAL_BIN"]

    for step in world.get("seed", []):
        if "sleep" in step:
            time.sleep(float(step["sleep"]))
            continue

        if "shell" in step:
            # Used to make the world contain things simpool did not create --
            # a simulator the user made by hand, for instance.
            command = resolve(step["shell"], facts)
            proc = subprocess.run(command, shell=True, capture_output=True, text=True, env=base_env)
            if proc.returncode != 0:
                raise SystemExit("seed shell %r failed: %s%s" % (command, proc.stdout, proc.stderr))
            if step.get("capture"):
                facts[step["capture"]] = {"stdout": proc.stdout.strip()}
            continue

        env = dict(base_env)
        if step.get("as"):
            env["SIMPOOL_BORROWER"] = step["as"]
        env.update({key: str(value) for key, value in step.get("env", {}).items()})

        argv = [resolve(token, facts) for token in step["run"]]
        proc = subprocess.run([binary] + argv, capture_output=True, text=True, env=env)

        if step.get("expect_failure") and proc.returncode == 0:
            raise SystemExit("seed step %r was expected to fail but succeeded" % argv)
        if not step.get("expect_failure") and proc.returncode != 0:
            raise SystemExit(
                "seed step %r failed (%d): %s%s" % (argv, proc.returncode, proc.stdout, proc.stderr)
            )

        if step.get("capture"):
            try:
                facts[step["capture"]] = json.loads(proc.stdout)
            except ValueError:
                facts[step["capture"]] = {"stdout": proc.stdout.strip()}

    path = os.path.join(os.environ["SANDBOX"], "logs", "seed-facts.json")
    with open(path, "w") as handle:
        json.dump(facts, handle, indent=2)

    return facts


def resolve(text, facts):
    """Substitute {{capture.path.to.field}} references in a string."""
    if not isinstance(text, str):
        return text

    out = text
    while "{{" in out and "}}" in out:
        start = out.index("{{")
        end = out.index("}}", start)
        path = out[start + 2 : end].strip()
        out = out[:start] + str(lookup(facts, path)) + out[end + 2 :]
    return out


def lookup(facts, path):
    node = facts
    for part in path.split("."):
        if isinstance(node, list):
            node = node[int(part)]
        else:
            node = node[part]
    return node


if __name__ == "__main__":
    run_seed(sys.argv[1])
