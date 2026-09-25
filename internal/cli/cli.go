// Package cli turns command-line arguments into pool operations and renders the
// result as either a human table or a stable JSON document.
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
	stdout io.Writer
	stderr io.Writer
	cfg    config.Config
	pool   *pool.Pool
	json   bool
}

type command struct {
	name    string
	summary string
	usage   string
	// bind registers the command's own flags and returns the action to run once
	// they are parsed. Keeping registration apart from the work lets the
	// dispatcher know --json before anything else can fail.
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
		printUsage(stdout)

		return errs.ExitInvalidArgs
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
		fmt.Fprintf(stderr, "simpool: unknown command %q\n\n", name)
		printUsage(stderr)

		return errs.ExitInvalidArgs
	}

	return runCommand(ctx, cmd, args[1:], stdout, stderr)
}

func runCommand(ctx context.Context, cmd command, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("simpool "+cmd.name, flag.ContinueOnError)
	flags.SetOutput(stderr)

	asJSON := flags.Bool("json", false, "emit a machine-readable JSON document")
	act := cmd.bind(flags)

	flags.Usage = func() {
		fmt.Fprintf(stderr, "Usage: simpool %s\n\n%s\n\nOptions:\n", cmd.usage, cmd.summary)
		flags.PrintDefaults()
	}

	// Options are parsed before any setup, so a failure to load the config or
	// open the database is still rendered in the format the caller asked for.
	positional, err := parseFlags(flags, args)

	environment := &env{
		stdout: stdout,
		stderr: stderr,
		json:   *asJSON,
	}

	// `--help` is handled by the flag package, which already printed the usage.
	if errors.Is(err, flag.ErrHelp) {
		return errs.ExitOK
	}

	var result any
	if err == nil {
		result, err = execute(ctx, cmd.needsPool, act, environment, positional)
	}

	if err != nil {
		writeError(stdout, stderr, cmd.name, err, environment.json)

		return errs.ExitCode(err)
	}

	if err := writeResult(stdout, cmd.name, result, environment.json); err != nil {
		fmt.Fprintf(stderr, "simpool %s: %s\n", cmd.name, err)

		return errs.ExitInternal
	}

	return exitStatusFor(result)
}

// execute loads the configuration, opens the pool when the command needs it,
// and runs the action.
func execute(ctx context.Context, needsPool bool, act action, environment *env, args []string) (any, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}

	environment.cfg = cfg

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

// exitStatusFor lets a successful command still report an unhealthy pool.
func exitStatusFor(result any) int {
	report, ok := result.(pool.DoctorReport)
	if ok && !report.Healthy {
		return errs.ExitInternal
	}

	return errs.ExitOK
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
Every command accepts --json. Run `+"`simpool <command> --help`"+` for options.

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
