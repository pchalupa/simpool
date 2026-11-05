# simpool

Share a host's iOS simulators between several AI agents. One agent borrows a
simulator, works on it, and gives it back.

```bash
npx @pchalupa/simpool init
```

This installs the `simpool` binary to `~/.local/bin`, then runs the
[skills CLI](https://github.com/vercel-labs/skills) so you can pick where the
`simpool` agent skill goes. Run
`npx @pchalupa/simpool init --help` for options.

macOS only. See the [repository](https://github.com/pchalupa/simpool) for usage.
