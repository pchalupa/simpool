#!/usr/bin/env python3
"""Put the pool into the state a scenario starts from.

`world.devices` simulators are created and imported first, so the pool has
something to lend. Then the scenario's `world.seed` list is replayed with the
real binary (not the logging shim) and a different borrower identity, so the
agent's own command log stays clean and the leases it meets look like another
agent's. Anything a seed command returns can be captured under a name and
referenced from the prompt or the checks as {{name.path.to.field}}.
"""

import json
import os
import subprocess
import sys
import time

POOL_DEVICE_TYPE = "com.apple.CoreSimulator.SimDeviceType.iPhone-17"
POOL_RUNTIME = "com.apple.CoreSimulator.SimRuntime.iOS-27-0"


def run_seed(scenario_path):
    with open(scenario_path) as handle:
        scenario = json.load(handle)

    world = scenario.get("world", {})
    facts = {}

    base_env = dict(os.environ, SIMPOOL_INTERNAL="1")
    base_env.update({key: str(value) for key, value in world.get("env", {}).items()})

    binary = os.environ["SIMPOOL_REAL_BIN"]

    add_pool_devices(binary, world.get("devices", 0), base_env)

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


def add_pool_devices(binary, count, env):
    """Create simulators the way a user would and import them into the pool.

    simpool never creates devices itself, so a world with a free device needs
    one made with simctl first.
    """
    for index in range(1, count + 1):
        created = subprocess.run(
            ["xcrun", "simctl", "create", "Pool iPhone %d" % index, POOL_DEVICE_TYPE, POOL_RUNTIME],
            capture_output=True,
            text=True,
            env=env,
        )
        if created.returncode != 0:
            raise SystemExit("creating pool device %d failed: %s" % (index, created.stderr))

        imported = subprocess.run(
            [binary, "import", created.stdout.strip()], capture_output=True, text=True, env=env
        )
        if imported.returncode != 0:
            raise SystemExit("importing pool device %d failed: %s%s" % (index, imported.stdout, imported.stderr))


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
