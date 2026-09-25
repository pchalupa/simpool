#!/usr/bin/env python3
"""Emit the shell environment for one scenario and write its fixture files.

run.sh evals this script's output, so every knob a scenario turns is set -- and
every knob it does not turn is explicitly unset, which keeps one scenario's
world from leaking into the next.
"""

import json
import os
import shlex
import stat
import sys

# Everything a scenario is allowed to set, so the rest can be cleared.
KNOBS = [
    "SIMPOOL_MAX_DEVICES",
    "SIMPOOL_LEASE_TTL",
    "SIMPOOL_OP_TIMEOUT",
    "SIMPOOL_BORROW_UNMANAGED",
    "SIMPOOL_DEFAULT_DEVICE_TYPE",
    "SIMPOOL_DEFAULT_RUNTIME",
    "SIMCTL_FAKE_FAIL",
    "SIMCTL_FAKE_LATENCY_MS",
    "SIMCTL_FAKE_VANISH_AFTER_CREATE",
]


def main(scenario_path):
    scenario = json.load(open(scenario_path))
    world = scenario.get("world", {})

    wanted = dict(world.get("env", {}))
    if "max_devices" in world:
        wanted["SIMPOOL_MAX_DEVICES"] = world["max_devices"]

    for knob in KNOBS:
        if knob in wanted:
            print("export %s=%s" % (knob, shlex.quote(str(wanted[knob]))))
        else:
            print("unset %s" % knob)

    # The agent's own identity, so checks can tell its leases from a seeded one.
    print("export SIMPOOL_BORROWER=eval-agent")

    work = os.path.join(os.environ["SANDBOX"], "work")
    for name, body in world.get("files", {}).items():
        path = os.path.join(work, name)
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "w") as handle:
            handle.write(body)
        if name.endswith(".sh"):
            os.chmod(path, os.stat(path).st_mode | stat.S_IXUSR | stat.S_IXGRP)


if __name__ == "__main__":
    main(sys.argv[1])
