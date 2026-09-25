# Evaluating simpool against a real agent

`simpool` exists to be driven by agents, so the thing worth measuring is not
whether the commands work — `go test` covers that — but whether an agent that
meets this CLI does the right thing with it: borrows instead of grabbing,
reacts to an error code instead of retrying, gives the device back.

Two suites answer two different questions.

| Suite | Question | Cost |
| --- | --- | --- |
| `./evals/race.sh` | Does the CLI hold its invariants when several agents hit one pool at once? | seconds, no model |
| `./evals/run.sh` | Does a real agent, given the shipped skill, use the CLI correctly? | minutes, one agent run per scenario |

Both run against a fake simulator host, so neither touches a real simulator, a
real Xcode, or `~/Library/Application Support/simpool`.

## The sandbox

`lib/xcrun` is a fake `xcrun`. simpool's iOS provider resolves `xcrun` through
`PATH` (`internal/provider/ios/ios.go`), so putting this script first gives the
harness a simulator host that is instant, deterministic, and available on a
machine with no Xcode — without one line of production code changing to
accommodate testing. It keeps its world in a JSON file under an exclusive
`flock`, so several racing simpool processes see a consistent host.

`lib/simpool-wrapper` is installed as `simpool` in the same directory. It logs
every invocation — argv, exit status, error code — to JSONL and then behaves
exactly like the real binary. Scoring reads that log rather than the agent's
transcript, which is what keeps the scenarios agent-agnostic: anything that
shells out to `simpool` can be scored by them.

Each run gets a sandbox in the system temp directory, never inside the
repository: Claude Code walks parent directories for `CLAUDE.md` and
`.claude/rules`, and a sandbox inside a checkout would silently hand the agent
instructions no scenario wrote. Logs, state and the fake host are copied back to
`evals/runs/<timestamp>/` when the run ends.

The fake host takes fault injection through the environment:
`SIMCTL_FAKE_FAIL=create:disk full`, `SIMCTL_FAKE_LATENCY_MS=250`,
`SIMCTL_FAKE_VANISH_AFTER_CREATE=1`.

## Concurrency suite

```bash
./evals/race.sh                 # every case
./evals/race.sh metro-port      # one case
RACE_AGENTS=16 ./evals/race.sh  # more pressure
```

Six cases, each in its own pool: eight agents racing for three devices, eight
agents all asking for Metro port 8081, a lapsed lease being picked up by the
next borrower, two processes releasing the same lease, a simulator deleted
behind simpool's back, and a simulator the user made by hand. It needs no model,
so it belongs in CI on every commit.

## Agent-behaviour suite

```bash
./evals/run.sh                        # every scenario, skill installed
./evals/run.sh --scenario 02          # one scenario (prefix match)
./evals/run.sh --arm both --judge     # skill vs. no-skill, plus the LLM judge
./evals/run.sh --repeat 3             # three runs each, to see the variance
```

Knobs: `EVAL_MODEL` (default `sonnet`), `EVAL_TIMEOUT` (seconds per run, default
600), `EVAL_JUDGE_MODEL` (default `opus`), `EVAL_KEEP_SANDBOX=1`, and `AGENT_CMD`
to drive an agent other than Claude Code — it is run with the prompt in
`$EVAL_PROMPT_FILE` and the same sandboxed `PATH`.

### Arms

`with-skill` copies `skills/simpool/SKILL.md` into the sandbox project.
`no-skill` leaves the project bare, so the agent meets only the binary, its
`--help` and its JSON. The gap between the two columns is the skill's
contribution; a scenario that scores well in both is one the CLI carries on its
own.

### What a scenario looks like

Each file in `scenarios/` describes a world, a task, and what must be true
afterwards. No scenario ever tells the agent to run simpool — the prompt is the
task a user would actually give, and using the pool is the behaviour under test.

```jsonc
{
  "id": "02-metro-port-conflict",
  "prompt": "I want to run this React Native app on a simulator ...",
  "world": {
    "max_devices": 4,
    "env": { "SIMPOOL_LEASE_TTL": "20s" },       // simpool's own configuration
    "seed": [                                    // what other agents already did
      { "as": "agent-b", "run": ["acquire", "--create-if-needed",
                                 "--meta", "metro-port=8081"],
        "capture": "other" }
    ],
    "files": { "package.json": "..." }           // the project the agent finds
  },
  "checks": [ /* deterministic, see below */ ],
  "judge":  [ "The agent told the user which Metro port it reserved ..." ]
}
```

Seed steps run as another borrower, with the real binary, so their commands stay
out of the agent's log. Anything a seed captures can be referenced from the
prompt or a check as `{{other.data.lease.id}}`.

### Checks

Deterministic, scored from the command log and the final pool state:

| kind | asserts |
| --- | --- |
| `ran` | the command was run, with given flags, successfully, within a count range |
| `never_ran` | a command (or a flag on it) was never used |
| `before` | one command preceded another |
| `no_blind_retry` | after a coded failure, the next command differed |
| `no_direct_simctl` | the agent never drove a simulator around the pool |
| `device_used` | the work ran against the UDID simpool handed out |
| `pool` | live leases, who holds them, how many devices survive |
| `lease_metadata` | a live lease records (or avoids) particular metadata |

`judge` criteria cover only what the agent *said*: whether it reported the
result honestly, explained a refusal, invented nothing. They are graded by a
second model from the same evidence a reviewer would read, and they never decide
what the agent *did* — that is what the checks are for.

### Output

`evals/runs/<timestamp>/summary.md` holds the scorecard: score per scenario per
arm, the failing checks with their evidence, and the cost of each arm in simpool
calls, wall clock and dollars. Every run keeps its prompt, the agent's final
message, the command log and the resulting database, so a failure can be read
back rather than re-run.

## Adding a scenario

Drop a JSON file in `scenarios/`. Give it a task a user would phrase that way,
a world that makes the interesting failure reachable, and checks that assert the
outcome rather than the transcript. A good scenario is one where an agent
without the skill plausibly does the wrong thing.
