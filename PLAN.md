# simpool implementation plan

## Goal

Build `simpool`, a host-local CLI that lets multiple AI agents share a pool of iOS simulators safely. An agent can acquire an available simulator, associate its worktree and React Native Metro port with the lease, renew the lease, and release it. The CLI can create and delete pool-owned simulators. Design the provider boundary so Android emulators can be added later.

The system consists of short-lived CLI invocations and a persistent SQLite database. There is no daemon.

## Product contract

- A simulator has a stable platform-specific ID (an iOS UDID), device type, runtime, observed state, and provenance: created by `simpool` or discovered externally.
- A lease has an opaque ID, device ID, borrower identifier, canonical worktree path (optional), Metro port (optional), acquisition time, and expiration time. Only one live lease may exist per device.
- `acquire` atomically reserves an available matching device. It can create one when explicitly allowed and within configured pool capacity; otherwise it returns a machine-readable capacity error. It returns both the device ID and lease ID.
- `renew` extends a live lease; `release` frees it. Expired leases are considered for reclamation when a subsequent command runs. No background heartbeat or cleanup is implied.
- Creating, booting, and deleting a simulator are distinct from leasing it. Pool-created devices may be deleted; externally discovered devices must not be deleted by `simpool`.
- CLI output supports `--json`, stable error codes, predictable exit statuses, and bounded operation timeouts. An agent should not need to parse human-readable `simctl` output itself.

## CLI surface (initial proposal)

```text
simpool list [--json]
simpool inspect <device-id> [--json]
simpool acquire --platform ios [--device-type TYPE] [--runtime RUNTIME]
                [--worktree PATH] [--metro-port PORT] [--create-if-needed] [--json]
simpool renew <lease-id> [--json]
simpool release <lease-id> [--json]
simpool create --platform ios --device-type TYPE --runtime RUNTIME [--json]
simpool delete <device-id> [--json]
simpool doctor [--json]
```

Use the returned device ID for downstream tooling. Define `release` as idempotent for an already released lease, but distinguish an unknown lease ID. Require the lease ID for mutating lease operations rather than trusting a worktree path as proof of ownership. Consider a separate `reclaim` command for explicit recovery instead of overloading `delete --force`.

## Architecture and persistence

1. **Go CLI:** Build a single executable. Keep command parsing/output, pool orchestration, persistence, and providers in separate packages. Prefer a pure-Go SQLite driver so installation does not require a C toolchain.
2. **SQLite state:** Store devices, leases, and in-progress operations. Use transactions and uniqueness constraints for exclusive leases across concurrent CLI processes. Keep migrations versioned. Avoid holding a write transaction while waiting for a simulator to boot: reserve work with an expiring operation record, do the external action, then commit its result; reconcile interrupted operations on the next invocation.
3. **Default location:** On macOS, use `~/Library/Application Support/simpool/pool.db`. Allow `SIMPOOL_STATE_DIR` to override it, including to a temporary directory for CI or deliberately ephemeral use. Do not default the authoritative database to a cache or OS temp directory, where it could disappear while devices remain. Cache only reconstructible data there. Specify a platform-appropriate state directory when Android support expands to other hosts.
4. **Provider interface:** Define operations to discover devices and runtimes, create, boot, wait for readiness, inspect health, shut down, and delete. Implement iOS via `xcrun simctl`; keep Android-specific fields and command details out of the lease layer. Do not build speculative Android behavior beyond this boundary in the first release.
5. **Reconciliation:** Compare persisted records to actual devices before allocating or acting on a device. Mark missing devices unavailable, recognize interrupted creates/boots, and surface actionable errors. Treat the database as authoritative for leases/provenance, but `simctl` as authoritative for actual device existence and runtime state.

## Lease and lifecycle policy

- Set a documented default lease TTL (proposed: two hours), configurable per acquire; agents renew it for longer sessions. Expiry alone does not shut down or delete a simulator.
- Reclaim an expired lease only after checking the device and updating state transactionally. An old lease ID must not renew or release a newer borrower's lease.
- Choose a consistent post-release policy: leave the device booted by default for fast reuse; allow an explicit shutdown or later configurable idle policy. Never silently delete on release.
- Canonicalize worktree paths. Treat worktree and Metro port as lease metadata, not ownership credentials. Detect a Metro port already recorded on another live lease and report a conflict; do not assume the CLI owns or supervises the Metro process.
- Bound the number of pool-owned devices via configuration. Provide explicit, safe deletion for idle pool-owned devices only. Show whether a device is unmanaged in `list` and `inspect`.
- Scope a pool to one host and one state directory. Sharing a database across machines is out of scope.

## Agent skill and distribution

Ship `skills/simpool/SKILL.md` in the project repository with a concise description that triggers for simulator provisioning, borrowing a simulator, iOS app testing, and React Native runs. Agents should follow this workflow: check that `simpool` is installed, acquire with `--json`, use the returned device and lease IDs, renew during long-running work, and release at completion or on failure. The skill must explain that creation/deletion are pool-maintenance actions and that unmanaged devices are protected. Prefer referencing CLI `--help` to duplicating a long option reference in the skill.

Document end-user skill installation via `npx skills` using the repository as the source. Document CLI installation separately: installing the skill does not put the binary on `PATH`. Verify the documented `npx skills` invocation against the released repository layout before publishing. Test that an agent with the skill installed selects `simpool` for a simulator task without being told the command.

## Delivery milestones

1. **Foundation:** Go project, CLI conventions, SQLite schema/migrations, configurable state directory, iOS discovery, `list`, `inspect`, `doctor`, JSON responses.
2. **Provisioning:** iOS create/boot/readiness/shutdown/delete, ownership provenance, interrupted-operation reconciliation, capacity and deletion guards.
3. **Leasing:** Atomic acquire/renew/release, expiry and reclaim rules, worktree/Metro metadata, concurrent-process tests, useful exit codes.
4. **Agent release:** Bundled skill, `npx skills` installation instructions, binary installation instructions, end-to-end agent workflow tests.
5. **Later:** Idle pool policies, richer diagnostics, Android provider and platform-specific host paths after validating the provider boundary with a small spike.

## Verification and acceptance criteria

- Two simultaneous `acquire` commands never return the same simulator; at capacity, the loser receives a deterministic error.
- A boot failure or killed CLI process does not permanently strand a reservation; the next operation can reconcile it.
- An expired lease can be reclaimed, and its former lease ID cannot affect a subsequent borrower.
- A simulator removed outside `simpool` is detected and reported rather than handed out.
- Active leases and externally created simulators cannot be deleted through ordinary `simpool delete`.
- A worktree and Metro port appear consistently in `list`, `inspect`, and the JSON acquire response; a conflicting live Metro port is rejected.
- State survives separate CLI processes and host restart when the default persistent directory is used; `SIMPOOL_STATE_DIR` isolates test pools.
- A freshly installed skill causes an agent to acquire and release via `simpool` for a relevant iOS/React Native task. A separate agent cannot borrow the device until it is released or its lease is reclaimed.

## Decisions to confirm during implementation

- Exact default lease TTL and whether `--create-if-needed` should be the default for `acquire` (proposed: explicit opt-in).
- Whether existing, externally created simulators may be borrowed by default or only after an explicit import (proposed: explicit import or opt-in, to avoid taking a user's personal device).
- Minimum supported macOS/Xcode versions and distribution channel for the Go binary.
