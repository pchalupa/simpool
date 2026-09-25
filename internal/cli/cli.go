// Package cli turns command-line arguments into pool operations and renders the
// result as a stable JSON document.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"runtime"
	"sort"

	"github.com/pchalupa/simpool/internal/config"
	"github.com/pchalupa/simpool/internal/errs"
	"github.com/pchalupa/simpool/internal/pool"
	"github.com/pchalupa/simpool/internal/provider"
	"github.com/pchalupa/simpool/internal/provider/ios"
	"github.com/pchalupa/simpool/internal/store"
)

// Version is the build version, overridden at link time.
var Version = "dev"

// env is the per-invocation context handed to a command implementation.
type env struct {
	cfg  config.Config
	pool *pool.Pool
}

type command struct {
	name    string
	summary string
	usage   string
	// bind registers the command's own flags and returns the action to run once
	// they are parsed.
	bind func(flags *flag.FlagSet) action
	// needsPool is false for commands that must work without a database or Xcode.
	needsPool bool
}

// action performs a command with its positional arguments. It returns a value
// to render, or a coded error.
type action func(ctx context.Context, env *env, args []string) (any, error)

// Run executes one CLI invocation and returns the process exit status.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		err := errs.New(errs.CodeInvalidArgs, "missing command; run `simpool --help` for the list")
		writeError(stdout, "", err)

		return errs.ExitCode(err)
	}

	name := args[0]

	switch name {
	case "-h", "--help", "help":
		printUsage(stdout)

		return errs.ExitOK
	case "-v", "--version", "version":
		fmt.Fprintf(stdout, "simpool %s\n", Version)

		return errs.ExitOK
	}

	cmd, ok := commands()[name]
	if !ok {
		err := errs.New(errs.CodeInvalidArgs, "unknown command %q; run `simpool --help` for the list", name)
		writeError(stdout, name, err)

		return errs.ExitCode(err)
	}

	return runCommand(ctx, cmd, args[1:], stdout, stderr)
}

func runCommand(ctx context.Context, cmd command, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("simpool "+cmd.name, flag.ContinueOnError)
	act := cmd.bind(flags)

	// A bad option is reported in the JSON document alone, so the flag package
	// must not print its own complaint and usage on the way out.
	flags.SetOutput(io.Discard)
	flags.Usage = func() {}

	positional, err := parseFlags(flags, args)

	if errors.Is(err, flag.ErrHelp) {
		flags.SetOutput(stderr)
		fmt.Fprintf(stderr, "Usage: simpool %s\n\n%s\n\nOptions:\n", cmd.usage, cmd.summary)
		flags.PrintDefaults()

		return errs.ExitOK
	}

	var result any
	if err == nil {
		result, err = execute(ctx, cmd.needsPool, act, positional)
	}

	if err != nil {
		writeError(stdout, cmd.name, err)

		return errs.ExitCode(err)
	}

	if err := writeResult(stdout, cmd.name, result); err != nil {
		writeError(stdout, cmd.name, err)

		return errs.ExitCode(err)
	}

	return errs.ExitOK
}

// execute loads the configuration, opens the pool when the command needs it,
// and runs the action.
func execute(ctx context.Context, needsPool bool, act action, args []string) (any, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}

	environment := &env{cfg: cfg}

	if !needsPool {
		return act(ctx, environment, args)
	}

	state, err := store.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return nil, err
	}
	defer state.Close()

	environment.pool = pool.New(cfg, state, newProvider(cfg))

	return act(ctx, environment, args)
}

// newProvider selects the platform implementation. Additional platforms plug in
// here without any other package learning about them.
func newProvider(cfg config.Config) provider.Provider {
	return ios.New(cfg.OpTimeout)
}

func printUsage(out io.Writer) {
	fmt.Fprintf(out, `simpool %s — share a pool of iOS simulators between agents.

Usage:
  simpool <command> [options]

Commands:
`, Version)

	registry := commands()
	names := make([]string, 0, len(registry))

	for name := range registry {
		names = append(names, name)
	}

	sort.Strings(names)

	for _, name := range names {
		fmt.Fprintf(out, "  %-10s %s\n", name, registry[name].summary)
	}

	fmt.Fprintf(out, `
Every command prints one JSON document to stdout with "ok", "command" and
either "data" or "error". Run `+"`simpool <command> --help`"+` for options.

Environment:
  %-28s authoritative state directory (default: %s)
  %-28s maximum pool-owned devices (default: %d)
  %-28s default lease TTL (default: %s)
  %-28s borrower identity recorded on leases
  %-28s timeout for a single platform operation (default: %s)
  %-28s device type used when creating without --device-type
  %-28s runtime used when creating without --runtime
  %-28s set to 1 to borrow externally created simulators without importing them
`,
		config.EnvStateDir, defaultStateDirHint(),
		config.EnvMaxDevices, config.DefaultMaxDevices,
		config.EnvLeaseTTL, config.DefaultLeaseTTL,
		config.EnvBorrower,
		config.EnvOpTimeout, config.DefaultOpTimeout,
		config.EnvDeviceType,
		config.EnvRuntime,
		config.EnvBorrowUnmanaged,
	)
}

func defaultStateDirHint() string {
	if runtime.GOOS == "darwin" {
		return "~/Library/Application Support/simpool"
	}

	return "~/.local/state/simpool"
}

// requireArg returns the single positional argument a command expects.
func requireArg(args []string, name string) (string, error) {
	if len(args) == 0 {
		return "", errs.New(errs.CodeInvalidArgs, "missing required argument <%s>", name)
	}

	if len(args) > 1 {
		return "", errs.New(errs.CodeInvalidArgs, "expected one <%s>, got %d arguments", name, len(args))
	}

	return args[0], nil
}
