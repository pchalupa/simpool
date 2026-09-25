#!/usr/bin/env python3
"""Score one finished scenario run.

Everything here is deterministic: it reads the command log the shim wrote, the
final pool state, and the scenario's `checks` list. Judgements that need reading
the agent's prose live in judge.py instead, so a failing check always points at
a fact rather than at an opinion.

Usage: check.py <scenario.json> <run-dir>
"""

import json
import os
import subprocess
import sys

from seed import lookup, resolve


def load_log(run_dir):
    path = os.path.join(run_dir, "logs", "simpool-commands.jsonl")
    if not os.path.exists(path):
        return []
    with open(path) as handle:
        return [json.loads(line) for line in handle if line.strip()]


def load_facts(run_dir):
    path = os.path.join(run_dir, "logs", "seed-facts.json")
    if not os.path.exists(path):
        return {}
    with open(path) as handle:
        return json.load(handle)


def pool_state(run_dir, scenario):
    """Ask simpool itself what the pool looks like now, with the real binary."""
    env = dict(os.environ)
    env["SIMPOOL_STATE_DIR"] = os.path.join(run_dir, "state")
    env["SIMCTL_STATE"] = os.path.join(run_dir, "state", "simctl.json")
    # Without the sandbox's fake xcrun ahead of the real one, this call would
    # reconcile the run's pool against the host's actual simulators and wipe
    # every lease the scenario is about.
    env["PATH"] = os.path.join(run_dir, "bin") + os.pathsep + env["PATH"]
    env["SIMPOOL_INTERNAL"] = "1"
    world = scenario.get("world", {})
    if "max_devices" in world:
        env["SIMPOOL_MAX_DEVICES"] = str(world["max_devices"])

    proc = subprocess.run(
        [env["SIMPOOL_REAL_BIN"], "list"], capture_output=True, text=True, env=env
    )
    try:
        # `list` returns data as an array of {device, lease?} views; a
        # released or expired lease is simply absent.
        return json.loads(proc.stdout).get("data") or []
    except ValueError:
        return []


def entries_for(log, cmd):
    return [entry for entry in log if entry["argv"] and entry["argv"][0] == cmd]


def has_flag(entry, flag):
    """True when the invocation carries the flag, in either --x=y or --x y form."""
    return any(token == flag or token.startswith(flag + "=") for token in entry["argv"])


def live_leases(views):
    """simpool only attaches a lease to a device view while that lease is live."""
    return [view["lease"] for view in views if view.get("lease")]


CHECKS = {}


def check(kind):
    def register(fn):
        CHECKS[kind] = fn
        return fn

    return register


@check("ran")
def check_ran(spec, ctx):
    """The agent ran this command, the required number of times, with these flags."""
    found = entries_for(ctx["log"], spec["cmd"])
    if spec.get("flags"):
        found = [e for e in found if all(has_flag(e, flag) for flag in spec["flags"])]
    if spec.get("args_contain"):
        wanted = [resolve(a, ctx["facts"]) for a in spec["args_contain"]]
        found = [e for e in found if all(any(w in token for token in e["argv"]) for w in wanted)]
    if spec.get("succeeded"):
        found = [e for e in found if e["exit"] == 0]

    low = spec.get("min", 1)
    high = spec.get("max")
    ok = len(found) >= low and (high is None or len(found) <= high)
    return ok, "%d matching `simpool %s` invocation(s)" % (len(found), spec["cmd"])


@check("never_ran")
def check_never_ran(spec, ctx):
    """The agent stayed away from a command (or a flag on it) entirely."""
    found = entries_for(ctx["log"], spec["cmd"])
    if spec.get("flag"):
        found = [e for e in found if has_flag(e, spec["flag"])]
    return not found, "%d invocation(s) of `simpool %s`" % (len(found), spec["cmd"])


@check("before")
def check_before(spec, ctx):
    """One command was run before another (acquire before work, work before release)."""
    first = entries_for(ctx["log"], spec["first"])
    then = entries_for(ctx["log"], spec["then"])
    if not first or not then:
        return False, "missing %s (%d) or %s (%d)" % (
            spec["first"], len(first), spec["then"], len(then),
        )
    return first[0]["ts"] < then[-1]["ts"], "ordering of first %s and last %s" % (
        spec["first"], spec["then"],
    )


@check("no_blind_retry")
def check_no_blind_retry(spec, ctx):
    """After a coded failure the agent changed something instead of repeating itself."""
    repeats = []
    for index, entry in enumerate(ctx["log"][:-1]):
        if entry["exit"] == 0:
            continue
        if spec.get("code") and entry.get("error_code") != spec["code"]:
            continue
        following = ctx["log"][index + 1]
        if following["argv"] == entry["argv"]:
            repeats.append(" ".join(entry["argv"]))
    return not repeats, ("no identical retry" if not repeats else "repeated verbatim: %s" % repeats)


@check("pool")
def check_pool(spec, ctx):
    """The pool it left behind: live leases, who holds them, how many devices exist."""
    views = ctx["state"]
    leases = live_leases(views)
    details = []
    ok = True

    if "live_leases" in spec:
        ok = ok and len(leases) == spec["live_leases"]
        details.append("%d live lease(s), expected %d" % (len(leases), spec["live_leases"]))

    if "live_leases_held_by" in spec:
        wanted = spec["live_leases_held_by"]
        mine = [l for l in leases if wanted in (l.get("borrower") or "")]
        ok = ok and bool(mine)
        details.append("%d lease(s) held by %r" % (len(mine), wanted))

    if "devices" in spec:
        count = len(views)
        ok = ok and count == spec["devices"]
        details.append("%d device(s), expected %d" % (count, spec["devices"]))

    if "device_survives" in spec:
        udid = resolve(spec["device_survives"], ctx["facts"])
        present = any(view["device"].get("id") == udid for view in views)
        ok = ok and present
        details.append("device %s present: %s" % (udid[:8], present))

    return ok, "; ".join(details)


@check("lease_metadata")
def check_lease_metadata(spec, ctx):
    """A live lease records what the task implied, and avoids what it must not claim."""
    leases = live_leases(ctx["state"])
    wanted = {k: resolve(v, ctx["facts"]) for k, v in spec.get("equals", {}).items()}
    forbidden = {k: resolve(v, ctx["facts"]) for k, v in spec.get("not_equals", {}).items()}

    for lease in leases:
        meta = lease.get("metadata") or {}
        if not all(str(meta.get(k, "")) == str(v) for k, v in wanted.items()):
            continue
        if any(str(meta.get(k, "")) == str(v) for k, v in forbidden.items()):
            continue
        if not all(meta.get(key) for key in spec.get("present", [])):
            continue
        return True, "lease metadata %s satisfies the scenario" % meta

    return False, "no live lease satisfies equals=%s not_equals=%s present=%s (saw %s)" % (
        wanted, forbidden, spec.get("present", []), [l.get("metadata") for l in leases],
    )


@check("device_used")
def check_device_used(spec, ctx):
    """The work ran against the device simpool handed out, not one the agent picked."""
    acquired = [
        json.loads(entry["stdout"])["data"]["device"]["id"]
        for entry in entries_for(ctx["log"], "acquire")
        if entry["exit"] == 0 and entry["stdout"].strip().startswith("{")
    ]
    path = os.path.join(ctx["run_dir"], "logs", spec.get("log", "work.log"))
    if not os.path.exists(path):
        return False, "the task's work log %s was never written" % spec.get("log", "work.log")
    body = open(path).read()
    hits = [udid for udid in acquired if udid in body]
    return bool(hits), "work log mentions %d of %d acquired device id(s)" % (len(hits), len(acquired))


@check("no_direct_simctl")
def check_no_direct_simctl(spec, ctx):
    """The agent never drove a simulator around the pool with its own simctl call."""
    path = os.path.join(ctx["run_dir"], "logs", "xcrun-direct.jsonl")
    forbidden = spec.get("subcommands", ["create", "boot", "shutdown", "delete", "erase"])
    offenders = []
    if os.path.exists(path):
        for line in open(path):
            if not line.strip():
                continue
            argv = json.loads(line)["argv"]
            if len(argv) >= 2 and argv[0] == "simctl" and argv[1] in forbidden:
                offenders.append(" ".join(argv))
    return not offenders, ("no direct simctl mutation" if not offenders else "bypassed the pool: %s" % offenders)


def main(scenario_path, run_dir):
    with open(scenario_path) as handle:
        scenario = json.load(handle)

    ctx = {
        "log": load_log(run_dir),
        "facts": load_facts(run_dir),
        "state": pool_state(run_dir, scenario),
        "scenario": scenario,
        "run_dir": run_dir,
    }

    results = []
    for spec in scenario.get("checks", []):
        handler = CHECKS.get(spec["kind"])
        if handler is None:
            results.append({"id": spec.get("id"), "text": spec.get("text", spec["kind"]),
                            "pass": False, "detail": "unknown check kind %r" % spec["kind"],
                            "weight": spec.get("weight", 1)})
            continue
        try:
            ok, detail = handler(spec, ctx)
        except Exception as error:  # a broken check must not look like a failing agent
            ok, detail = False, "check raised %s: %s" % (type(error).__name__, error)
        results.append({
            "id": spec.get("id"),
            "text": spec.get("text", spec["kind"]),
            "pass": bool(ok),
            "detail": detail,
            "weight": spec.get("weight", 1),
        })

    earned = sum(r["weight"] for r in results if r["pass"])
    total = sum(r["weight"] for r in results) or 1

    report = {
        "scenario": scenario["id"],
        "checks": results,
        "score": round(earned / total, 3),
        "metrics": {
            "simpool_calls": len(ctx["log"]),
            "failed_calls": sum(1 for e in ctx["log"] if e["exit"] != 0),
            "error_codes": sorted({e["error_code"] for e in ctx["log"] if e.get("error_code")}),
            "cli_time_ms": round(sum(e.get("duration_ms", 0) for e in ctx["log"]), 1),
            "commands": [" ".join(e["argv"]) for e in ctx["log"]],
        },
    }
    print(json.dumps(report, indent=2))


if __name__ == "__main__":
    main(sys.argv[1], sys.argv[2])
