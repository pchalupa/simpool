---
name: simpool
description: Borrow an iOS simulator from the host's shared pool instead of picking one yourself. Use when a task needs a simulator to run, build, test or QA an iOS or React Native app, when several agents share one machine, or when a simulator must be released or reclaimed.
allowed-tools: Bash(simpool:*)
---

# Borrowing a simulator with simpool

`simpool` hands out iOS simulators to one borrower at a time and records the
lease in a host-local database. Several agents can run on one Mac without
fighting over a device.

Borrow a device before using any simulator tooling, and release it when the work
ends. A simulator you picked yourself may already belong to another agent.

## Workflow

1. **Check it is installed:** `command -v simpool`. If it is absent, tell the
   user simpool is not installed and continue with your usual simulator
   workflow; do not install it yourself.
2. **Acquire**:

   ```bash
   simpool acquire
   ```

   Record `data.device.id` and `data.lease.id` from the response. Add
   `--runtime` or `--device-type` only when the task names one.

   Metadata is a free-form place to note what you are doing with the device,
   so you and other agents can tell simulators apart in `simpool list`. Add it
   only when it is true of this lease:

   ```bash
   simpool acquire --meta worktree="$PWD" --meta metro-port=8081
   ```

   Values are stored as-is and never checked against other leases. A lease
   with no metadata is perfectly normal.
3. **Work against the returned device ID.** It is a normal simulator UDID: pass
   it to argent, `xcrun simctl`, `npx react-native run-ios --udid`, or
   `xcodebuild -destination "id=<device-id>"`. The device is already booted
   unless you passed `--no-boot`.
4. **Renew** while the work continues: `simpool renew <lease-id>`. Leases
   expire (two hours by default) and an expired lease is handed to the next
   agent, so renew before a long build or test run rather than after it. Renew
   also merges metadata, so you can record something you learned later:
   `simpool renew <lease-id> --meta metro-port=8081`.
5. **Release** when the task ends, including when it fails:
   `simpool release <lease-id>`. Releasing twice is safe. The device is
   left booted for the next borrower; add `--shutdown` only if the user asked
   for a cold device.

## Reading failures

Every command prints a JSON document. A failure exits non-zero with a stable
`error.code`. Act on the code, not on the message text:

| code | what to do |
| --- | --- |
| `no_device_available` | Every simulator in the pool is leased, or none is in the pool yet. Release your own leases if you hold any, otherwise tell the user. |
| `lease_not_live` | Your lease expired or was reclaimed. Acquire again; the old lease ID is inert. |
| `device_leased` | Another agent holds the device. Pick another one; do not force it. |
| `provider_error`, `timeout` | Xcode or simctl failed. Report the message to the user rather than retrying blindly. |

A device you could not borrow is not yours to use. When `acquire` fails and the
table leaves nothing to try, stop and tell the user why. Do not fall back to a
simulator you found yourself, not even the user's own: it may be another
agent's, and a device nobody leased can be taken from you mid-task. Do not
create a simulator yourself either.

## Pool maintenance is not borrowing

`import` and `reclaim` change the pool itself. Run them when the user asks for
them, not as a way around a failed acquire:

- `simpool import <device-id>` makes one of the user's existing simulators
  borrowable. Ask first: it opts their device into the pool.
- `simpool reclaim <device-id>` frees a device whose borrower is gone. Without
  `--force` it only touches leases that have already expired.

`simpool list` shows who holds what.

Run `simpool <command> --help` for the full option list.
