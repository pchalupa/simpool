package cli

import (
	"context"
	"flag"
	"sort"
	"strings"

	"github.com/strv/simpool/internal/errs"
	"github.com/strv/simpool/internal/pool"
	"github.com/strv/simpool/internal/provider"
)

// commands is the CLI surface. Mutating lease operations take the lease ID: a
// worktree path is metadata, never proof of ownership.
func commands() map[string]command {
	return map[string]command{
		"list": {
			name:      "list",
			summary:   "List every device this pool knows, with its lease",
			usage:     "list [--json]",
			needsPool: true,
			run: func(ctx context.Context, environment *env, flags *flag.FlagSet, args []string) (any, error) {
				if err := parseNoArgs(flags, args); err != nil {
					return nil, err
				}

				return environment.pool.List(ctx)
			},
		},
		"inspect": {
			name:      "inspect",
			summary:   "Show one device, its provenance and its lease",
			usage:     "inspect <device-id> [--json]",
			needsPool: true,
			run: func(ctx context.Context, environment *env, flags *flag.FlagSet, args []string) (any, error) {
				deviceID, err := parseOneArg(flags, args, "device-id")
				if err != nil {
					return nil, err
				}

				return environment.pool.Inspect(ctx, deviceID)
			},
		},
		"acquire": {
			name:      "acquire",
			summary:   "Reserve a matching simulator and return its device and lease IDs",
			usage:     "acquire [--device-type TYPE] [--runtime RUNTIME] [--meta KEY=VALUE]... [--create-if-needed]",
			needsPool: true,
			run:       runAcquire,
		},
		"renew": {
			name:      "renew",
			summary:   "Extend a live lease before it expires",
			usage:     "renew <lease-id> [--ttl DURATION] [--meta KEY=VALUE]...",
			needsPool: true,
			run:       runRenew,
		},
		"release": {
			name:      "release",
			summary:   "Free a lease; the device is left booted unless --shutdown",
			usage:     "release <lease-id> [--shutdown]",
			needsPool: true,
			run:       runRelease,
		},
		"create": {
			name:      "create",
			summary:   "Create a pool-owned simulator without leasing it",
			usage:     "create [--device-type TYPE] [--runtime RUNTIME]",
			needsPool: true,
			run:       runCreate,
		},
		"delete": {
			name:      "delete",
			summary:   "Delete an idle pool-owned simulator (never an external one)",
			usage:     "delete <device-id> [--force]",
			needsPool: true,
			run:       runDelete,
		},
		"import": {
			name:      "import",
			summary:   "Allow an externally created simulator to be borrowed",
			usage:     "import <device-id> [--undo]",
			needsPool: true,
			run:       runImport,
		},
		"reclaim": {
			name:      "reclaim",
			summary:   "Recover a device whose borrower is gone",
			usage:     "reclaim <device-id> [--force]",
			needsPool: true,
			run:       runReclaim,
		},
		"shutdown": {
			name:      "shutdown",
			summary:   "Power a simulator off without changing its lease",
			usage:     "shutdown <device-id>",
			needsPool: true,
			run: func(ctx context.Context, environment *env, flags *flag.FlagSet, args []string) (any, error) {
				deviceID, err := parseOneArg(flags, args, "device-id")
				if err != nil {
					return nil, err
				}

				return environment.pool.Shutdown(ctx, deviceID)
			},
		},
		"doctor": {
			name:      "doctor",
			summary:   "Check the toolchain, the state directory and pool health",
			usage:     "doctor [--json]",
			needsPool: true,
			run: func(ctx context.Context, environment *env, flags *flag.FlagSet, args []string) (any, error) {
				if err := parseNoArgs(flags, args); err != nil {
					return nil, err
				}

				return environment.pool.Doctor(ctx)
			},
		},
	}
}

func runAcquire(ctx context.Context, environment *env, flags *flag.FlagSet, args []string) (any, error) {
	platform := flags.String("platform", "ios", "platform to borrow from")
	deviceType := flags.String("device-type", "", "device type identifier, name or unambiguous fragment")
	runtime := flags.String("runtime", "", "runtime identifier, name or version (e.g. 26.5)")
	metadata := newMetaFlag()
	flags.Var(metadata, "meta", metadataUsage)
	borrower := flags.String("borrower", "", "borrower identity recorded on the lease")
	ttl := flags.Duration("ttl", 0, "lease duration (default: the configured TTL)")
	createIfNeeded := flags.Bool("create-if-needed", false, "create a pool-owned device when none is free")
	noBoot := flags.Bool("no-boot", false, "do not boot the device after leasing it")

	if err := parseNoArgs(flags, args); err != nil {
		return nil, err
	}

	return environment.pool.Acquire(ctx, pool.AcquireRequest{
		Platform:       *platform,
		DeviceType:     *deviceType,
		Runtime:        *runtime,
		Metadata:       metadata.withoutEmptyValues(),
		Borrower:       *borrower,
		TTL:            *ttl,
		CreateIfNeeded: *createIfNeeded,
		Boot:           !*noBoot,
	})
}

func runRenew(ctx context.Context, environment *env, flags *flag.FlagSet, args []string) (any, error) {
	ttl := flags.Duration("ttl", 0, "new lease duration measured from now")
	metadata := newMetaFlag()
	flags.Var(metadata, "meta", metadataUsage+"; an empty value removes the key")

	leaseID, err := parseOneArg(flags, args, "lease-id")
	if err != nil {
		return nil, err
	}

	return environment.pool.Renew(ctx, pool.RenewRequest{
		LeaseID:  leaseID,
		TTL:      *ttl,
		Metadata: metadata.values,
	})
}

func runRelease(ctx context.Context, environment *env, flags *flag.FlagSet, args []string) (any, error) {
	shutdown := flags.Bool("shutdown", false, "power the device off after releasing it")

	leaseID, err := parseOneArg(flags, args, "lease-id")
	if err != nil {
		return nil, err
	}

	return environment.pool.Release(ctx, leaseID, *shutdown)
}

func runCreate(ctx context.Context, environment *env, flags *flag.FlagSet, args []string) (any, error) {
	deviceType := flags.String("device-type", "", "device type identifier, name or unambiguous fragment")
	runtime := flags.String("runtime", "", "runtime identifier, name or version (e.g. 26.5)")
	platform := flags.String("platform", "ios", "platform to create on")

	if err := parseNoArgs(flags, args); err != nil {
		return nil, err
	}

	if err := requirePlatform(*platform); err != nil {
		return nil, err
	}

	return environment.pool.Create(ctx, *deviceType, *runtime)
}

func runDelete(ctx context.Context, environment *env, flags *flag.FlagSet, args []string) (any, error) {
	force := flags.Bool("force", false, "delete a pool-owned device even while it is leased")

	deviceID, err := parseOneArg(flags, args, "device-id")
	if err != nil {
		return nil, err
	}

	return environment.pool.Delete(ctx, deviceID, *force)
}

func runImport(ctx context.Context, environment *env, flags *flag.FlagSet, args []string) (any, error) {
	undo := flags.Bool("undo", false, "stop the pool from handing this device out")

	deviceID, err := parseOneArg(flags, args, "device-id")
	if err != nil {
		return nil, err
	}

	return environment.pool.Import(ctx, deviceID, *undo)
}

func runReclaim(ctx context.Context, environment *env, flags *flag.FlagSet, args []string) (any, error) {
	force := flags.Bool("force", false, "reclaim even a lease that has not expired")

	deviceID, err := parseOneArg(flags, args, "device-id")
	if err != nil {
		return nil, err
	}

	return environment.pool.Reclaim(ctx, deviceID, *force)
}

// parseFlags parses a command's own options and returns its positional
// arguments. Commands register their flags inside run, so parsing happens there
// rather than in the dispatcher.
//
// Options are accepted before and after the positional argument, because
// `simpool renew <lease-id> --ttl 30m` is how people and agents actually write
// it; Go's flag package stops at the first non-flag argument on its own.
func parseFlags(flags *flag.FlagSet, args []string) ([]string, error) {
	positional := []string{}
	remaining := args

	for {
		if err := flags.Parse(remaining); err != nil {
			return nil, errs.Wrap(err, errs.CodeInvalidArgs, "parsing options")
		}

		remaining = flags.Args()
		if len(remaining) == 0 {
			return positional, nil
		}

		positional = append(positional, remaining[0])
		remaining = remaining[1:]
	}
}

func parseNoArgs(flags *flag.FlagSet, args []string) error {
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}

	return requireNoArgs(positional)
}

func parseOneArg(flags *flag.FlagSet, args []string, name string) (string, error) {
	positional, err := parseFlags(flags, args)
	if err != nil {
		return "", err
	}

	return requireArg(positional, name)
}

func requireNoArgs(args []string) error {
	if len(args) > 0 {
		return errs.New(errs.CodeInvalidArgs, "unexpected argument %q", args[0])
	}

	return nil
}

func requirePlatform(platform string) error {
	if platform != provider.PlatformIOS {
		return errs.New(errs.CodeInvalidArgs, "platform %q is not supported by this build", platform)
	}

	return nil
}

// metadataUsage documents the free-form lease metadata. Nothing is recorded
// unless the caller asks for it.
const metadataUsage = "lease metadata as `key=value`; repeatable. " +
	"simpool acts on worktree and metro_port, and stores any other key as-is"

// metaFlag collects repeated --meta key=value options.
type metaFlag struct {
	values map[string]string
}

func newMetaFlag() *metaFlag {
	return &metaFlag{values: map[string]string{}}
}

func (m *metaFlag) String() string {
	pairs := make([]string, 0, len(m.values))
	for key, value := range m.values {
		pairs = append(pairs, key+"="+value)
	}

	sort.Strings(pairs)

	return strings.Join(pairs, ",")
}

func (m *metaFlag) Set(raw string) error {
	key, value, found := strings.Cut(raw, "=")
	if !found {
		return errs.New(errs.CodeInvalidArgs, "metadata must be written as key=value, got %q", raw)
	}

	key = strings.TrimSpace(key)
	if key == "" {
		return errs.New(errs.CodeInvalidArgs, "metadata key is empty in %q", raw)
	}

	m.values[key] = value

	return nil
}

// withoutEmptyValues drops keys given with no value. On acquire there is
// nothing to record; on renew the same spelling removes the key instead.
func (m *metaFlag) withoutEmptyValues() map[string]string {
	kept := make(map[string]string, len(m.values))

	for key, value := range m.values {
		if value != "" {
			kept[key] = value
		}
	}

	return kept
}
