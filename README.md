# simpool

`simpool` shares a host's iOS simulators between several AI agents. One agent
borrows a simulator, works on it, and gives it back. Nobody else gets that
device while the lease is live.

There is no daemon. Every command is a short-lived process that reads and writes
one SQLite database, so state survives between invocations and across a restart.

## Why

Run two or three coding agents on the same Mac and they will happily boot the
same simulator, install over each other's builds, and start Metro twice on port
8081. `simpool` gives each agent its own device and records who holds what.

## Install

```bash
go install github.com/pchalupa/simpool/cmd/simpool@latest
```

Or build from a checkout:

```bash
make build      # writes ./bin/simpool
make install    # go install into $GOBIN
```

You need macOS with Xcode and at least one simulator runtime installed.

Installing the agent skill does not install the binary. Both steps are separate,
see [Agent skill](#agent-skill).

## Quick start

```bash
# borrow a simulator, creating one if the pool has none free
simpool acquire --create-if-needed

# work with the device ID the command returned
xcrun simctl install <device-id> MyApp.app

# keep the lease alive during long work
simpool renew <lease-id>

# give it back (the device stays booted for the next borrower)
simpool release <lease-id>
```

Running a React Native app? Record the worktree and the Metro port you will
serve on, so another agent does not take the same port:

```bash
simpool acquire --create-if-needed \
  --meta worktree="$PWD" --meta metro-port=8081
```

Every command prints one JSON document on stdout with `ok`, `command` and
either `data` or `error`. Failures are JSON too, so a caller reads one stream.

## Commands

| Command | What it does |
| --- | --- |
| `list` | Every device the pool knows, with its lease |
| `acquire` | Reserve a matching device and return its device and lease IDs |
| `renew <lease-id>` | Extend a live lease |
| `release <lease-id>` | Free a lease |
| `create` | Add a pool-owned device without leasing it |
| `delete <device-id>` | Delete a pool-owned device |
| `import <device-id>` | Let the pool hand out a simulator you created |
| `reclaim <device-id>` | Free a device whose borrower is gone |
| `shutdown <device-id>` | Power a device off |

Run `simpool <command> --help` for options.

## How leasing works

A **lease** is an exclusive claim on one device. It has an opaque ID, a
borrower, an optional worktree path, an optional Metro port, and an expiry.

- Only one live lease can exist per device. The database enforces it, so two
  `acquire` commands racing each other can never win the same simulator. The
  loser gets `no_device_available` or `no_capacity`.
- The default lease runs for two hours. Change it with `--ttl` or
  `SIMPOOL_LEASE_TTL`.
- An expired lease is released the next time any command runs. Expiry never
  shuts a simulator down and never deletes it.
- A stale lease ID is inert. It cannot renew or release the lease that the next
  borrower now holds.
- Mutating a lease always needs the lease ID. Metadata is never proof of
  ownership.
- Releasing leaves the device booted so the next borrower starts fast. Pass
  `--shutdown` to power it off instead.

## Lease metadata

A lease carries a free-form bag of `key=value` pairs. It is entirely optional:
most leases have none at all.

```bash
simpool acquire --meta worktree="$PWD" --meta metro-port=8081 --meta branch=feature/login
```

Keys are lowercased and dashes become underscores, so `--meta metro-port=8081`
is stored as `metro_port`. Values are strings. A lease holds at most 16 keys.

Two keys mean something to `simpool`, and only when you set them:

- `worktree` is canonicalized to an absolute path with symlinks resolved, so
  every agent records the same checkout the same way.
- `metro_port` must be a port between 1 and 65535, and no two live leases may
  record the same one. The second one gets `metro_port_conflict`, with the
  conflicting lease in `error.details`. `simpool` does not start, own or watch
  the Metro process; it only stops two agents from claiming one port.

Every other key is stored and returned untouched. Use them for whatever helps
you find a device later: a branch, a ticket, an app bundle ID.

`renew` merges metadata into what the lease already holds, so you can add a
Metro port after Metro actually starts. An empty value removes a key:

```bash
simpool renew <lease-id> --meta metro-port=8082   # add or change
simpool renew <lease-id> --meta metro-port=       # remove
```

## Provenance: pool devices and your devices

Every device is either **pool-owned** (created by `simpool`, named
`simpool-op_...`) or **external** (it was already on your Mac).

- External simulators are never handed out until you run
  `simpool import <device-id>`. Your personal device stays yours.
- External simulators are never deleted by `simpool`, with or without `--force`.
- `delete` only removes pool-owned devices, and refuses while a lease is live
  unless you pass `--force`.
- `list` shows which is which in each device's `provenance` field.

`SIMPOOL_MAX_DEVICES` (default 4) bounds how many pool-owned devices can exist.
`acquire --create-if-needed` returns `no_capacity` at the limit instead of
filling your disk with simulators.

## Reconciliation

`simctl` is the truth about which simulators exist and what state they are in.
The database is the truth about leases and provenance. Before allocating
anything, `simpool` compares the two:

- A simulator deleted outside `simpool` is marked missing and reported, not
  handed out. Its lease is closed.
- A device left behind by a killed CLI process is adopted into the pool. Every
  create is reserved in the database first and named after that reservation, so
  the leftover device is recognisable.
- A reservation that never produced a device is failed once its deadline passes,
  which frees the capacity slot again.

## State and configuration

The database lives in `~/Library/Application Support/simpool/pool.db`. It is
deliberately not in a cache or temp directory: the devices it tracks outlive
both.

| Variable | Default | Meaning |
| --- | --- | --- |
| `SIMPOOL_STATE_DIR` | `~/Library/Application Support/simpool` | Where the database lives. Point it at a temp directory for tests. |
| `SIMPOOL_MAX_DEVICES` | `4` | Maximum pool-owned devices |
| `SIMPOOL_LEASE_TTL` | `2h` | Default lease duration |
| `SIMPOOL_BORROWER` | user@host/pid | Borrower recorded on leases |
| `SIMPOOL_OP_TIMEOUT` | `5m` | Timeout for one simctl operation |
| `SIMPOOL_DEFAULT_DEVICE_TYPE` | newest plain iPhone | Device type used when creating |
| `SIMPOOL_DEFAULT_RUNTIME` | newest installed | Runtime used when creating |
| `SIMPOOL_BORROW_UNMANAGED` | off | Borrow external simulators without importing them |

A pool covers one host and one state directory. Sharing a database between
machines is out of scope.

## Exit codes and error codes

Each failure carries a stable `error.code` in JSON and a matching exit status:

| Exit | Meaning | Codes |
| --- | --- | --- |
| 0 | Success | |
| 1 | Internal or state error | `internal_error`, `state_error` |
| 2 | Bad arguments | `invalid_args` |
| 3 | Unknown device or lease | `device_not_found`, `lease_not_found` |
| 4 | Conflict | `device_leased`, `device_unmanaged`, `lease_not_live`, `metro_port_conflict` |
| 5 | Nothing to hand out | `no_device_available`, `no_capacity` |
| 6 | Xcode or simctl failed | `provider_error` |
| 7 | Operation timed out | `timeout` |

## Agent skill

The repository ships an agent skill in `skills/simpool/`. Install it with:

```bash
npx skills add strv/simpool
```

Verify the exact invocation against the published repository before you rely on
it. The skill teaches an agent to acquire, renew and release, and explains that
creation and deletion are maintenance actions. It does not install the binary,
so install `simpool` separately and make sure it is on `PATH`.

## Layout

```
cmd/simpool          entry point
internal/cli         argument parsing and JSON output
internal/pool        leasing, capacity, reconciliation
internal/store       SQLite schema, migrations, queries
internal/provider    the platform boundary
internal/provider/ios    simctl implementation
internal/provider/fake   in-memory provider used by tests
skills/simpool       the bundled agent skill
```

Adding Android means implementing `provider.Provider` and choosing a state
directory for the host platform. The lease layer knows nothing about simctl.

## Development

```bash
make test       # go test -race ./...
make lint       # gofmt check and go vet
make build
```

Tests run against the in-memory provider, so they need no Xcode. They cover
concurrent acquires from separate database handles, expiry and reclaim, deletion
guards, Metro port conflicts and interrupted creates.

## Evaluation

The unit tests say the commands work. `evals/` asks the other question: does an
agent that meets this CLI actually borrow, react to an error code, and give the
device back?

```bash
make eval-race                        # CLI invariants under concurrent agents
make eval                             # a real agent, per scenario, with the skill
EVAL_ARGS="--arm both --judge" make eval   # skill vs. no skill, plus an LLM judge
```

Both suites run against a fake `xcrun` on `PATH`, so they touch no real
simulator and need no Xcode. `evals/README.md` explains the sandbox, the
scenario format and the checks.
