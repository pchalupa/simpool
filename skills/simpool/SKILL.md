---
name: simpool
description: Borrow an iOS simulator from the host's shared pool instead of picking one yourself. Use when a task needs a simulator to run, build, test or QA an iOS or React Native app, when several agents share one machine, when a Metro port must not clash with another agent's, or when a simulator must be created, released, reclaimed or deleted.
allowed-tools: Bash(simpool:*)
---

# Borrowing a simulator with simpool

`simpool` hands out iOS simulators to one borrower at a time and records the
lease in a host-local database. Several agents can run on one Mac without
fighting over a device or a Metro port.

Borrow a device before using any simulator tooling, and release it when the work
ends. A simulator you picked yourself may already belong to another agent.

## Workflow

1. **Check it is installed:** `command -v simpool`. If it is absent, tell the
   user simpool is not installed and continue with your usual simulator
   workflow; do not install it yourself.
2. **Acquire**, always with `--json`:

   ```bash
   simpool acquire --create-if-needed --json
   ```

   Record `data.device.id` and `data.lease.id` from the response. Add
   `--runtime` or `--device-type` only when the task names one.

   Add metadata only when it is true of this lease:

   ```bash
   simpool acquire --create-if-needed \
     --meta worktree="$PWD" --meta metro-port=8081 --json
   ```

   `--meta metro-port=` claims the port, so record it only when you will
   actually start Metro on it. A port another live lease already holds is
   rejected. `--meta worktree=` records the checkout. Any other key is stored
   as-is. A lease with no metadata is perfectly normal.
3. **Work against the returned device ID.** It is a normal simulator UDID: pass
   it to argent, `xcrun simctl`, `npx react-native run-ios --udid`, or
   `xcodebuild -destination "id=<device-id>"`. The device is already booted
   unless you passed `--no-boot`.
4. **Renew** while the work continues: `simpool renew <lease-id> --json`. Leases
   expire (two hours by default) and an expired lease is handed to the next
   agent, so renew before a long build or test run rather than after it. Renew
   also merges metadata, which is how you record a Metro port once Metro is
   actually running: `simpool renew <lease-id> --meta metro-port=8081`.
5. **Release** when the task ends, including when it fails:
   `simpool release <lease-id> --json`. Releasing twice is safe. The device is
   left booted for the next borrower; add `--shutdown` only if the user asked
   for a cold device.

## Reading failures

Every command exits non-zero with a stable `error.code` in its JSON. Act on the
code, not on the message text:

| code | what to do |
| --- | --- |
| `no_device_available` | Retry with `--create-if-needed`. If that fails too, the pool is full. |
| `no_capacity` | The pool is at its device limit. Release your own leases, or tell the user which limit is in the way (`error.details.max_devices`). |
| `metro_port_conflict` | Another live lease holds that port. Acquire with a different `--meta metro-port=`. |
| `lease_not_live` | Your lease expired or was reclaimed. Acquire again; the old lease ID is inert. |
| `device_leased` | Another agent holds the device. Pick another one; do not force it. |
| `device_unmanaged` | The device was created by the user, not by simpool. It is protected. |
| `provider_error`, `timeout` | Xcode or simctl failed. Report the message to the user rather than retrying blindly. |

## Pool maintenance is not borrowing

`create`, `delete`, `import` and `reclaim` change the pool itself. Run them when
the user asks for them, not as a way around a failed acquire:

- `simpool create` adds a pool-owned device without leasing it. `acquire
  --create-if-needed` already covers the borrowing case.
- `simpool delete <device-id>` removes a pool-owned device. Simulators the user
  created are never deleted, with or without `--force`.
- `simpool import <device-id>` makes one of the user's existing simulators
  borrowable. Ask first: it opts their personal device into the pool.
- `simpool reclaim <device-id>` frees a device whose borrower is gone. Without
  `--force` it only touches leases that have already expired.

`simpool list` and `simpool inspect <device-id>` show who holds what, and
`simpool doctor` explains an unhealthy pool.

Run `simpool <command> --help` for the full option list.
