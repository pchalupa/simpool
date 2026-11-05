package cli

import (
	"context"
	"flag"
	"sort"
	"strings"

	"github.com/pchalupa/simpool/internal/errs"
	"github.com/pchalupa/simpool/internal/pool"
)

// commands is the CLI surface. Mutating lease operations take the lease ID:
// metadata is never proof of ownership.
func commands() map[string]command {
	return map[string]command{
		"list": {
			name:      "list",
			summary:   "List every device this pool knows, with its lease",
			usage:     "list",
			needsPool: true,
			bind: noFlags(func(ctx context.Context, environment *env, args []string) (any, error) {
				if err := requireNoArgs(args); err != nil {
					return nil, err
				}

				return environment.pool.List(ctx)
			}),
		},
		"acquire": {
			name:      "acquire",
			summary:   "Reserve a matching simulator and return its device and lease IDs",
			usage:     "acquire [--device-type TYPE] [--runtime RUNTIME] [--meta KEY=VALUE]...",
			needsPool: true,
			bind:      bindAcquire,
		},
		"renew": {
			name:      "renew",
			summary:   "Extend a live lease before it expires",
			usage:     "renew <lease-id> [--ttl DURATION] [--meta KEY=VALUE]...",
			needsPool: true,
			bind:      bindRenew,
		},
		"release": {
			name:      "release",
			summary:   "Free a lease; the device is left booted unless --shutdown",
			usage:     "release <lease-id> [--shutdown]",
			needsPool: true,
			bind:      bindRelease,
		},
		"import": {
			name:      "import",
			summary:   "Allow an existing simulator to be borrowed",
			usage:     "import <device-id> [--undo]",
			needsPool: true,
			bind:      bindImport,
		},
		"reclaim": {
			name:      "reclaim",
			summary:   "Recover a device whose borrower is gone",
			usage:     "reclaim <device-id> [--force]",
			needsPool: true,
			bind:      bindReclaim,
		},
		"shutdown": {
			name:      "shutdown",
			summary:   "Power a simulator off without changing its lease",
			usage:     "shutdown <device-id>",
			needsPool: true,
			bind: noFlags(func(ctx context.Context, environment *env, args []string) (any, error) {
				deviceID, err := requireArg(args, "device-id")
				if err != nil {
					return nil, err
				}

				return environment.pool.Shutdown(ctx, deviceID)
			}),
		},
	}
}

// noFlags binds a command that has no options.
func noFlags(act action) func(*flag.FlagSet) action {
	return func(*flag.FlagSet) action {
		return act
	}
}

func bindAcquire(flags *flag.FlagSet) action {
	platform := flags.String("platform", "ios", "platform to borrow from")
	deviceType := flags.String("device-type", "", "device type identifier, name or unambiguous fragment")
	runtime := flags.String("runtime", "", "runtime identifier, name or version (e.g. 26.5)")
	metadata := newMetaFlag()
	flags.Var(metadata, "meta", metadataUsage)
	borrower := flags.String("borrower", "", "borrower identity recorded on the lease")
	ttl := flags.Duration("ttl", 0, "lease duration (default: the configured TTL)")
	noBoot := flags.Bool("no-boot", false, "do not boot the device after leasing it")

	return func(ctx context.Context, environment *env, args []string) (any, error) {
		if err := requireNoArgs(args); err != nil {
			return nil, err
		}

		return environment.pool.Acquire(ctx, pool.AcquireRequest{
			Platform:   *platform,
			DeviceType: *deviceType,
			Runtime:    *runtime,
			Metadata:   metadata.withoutEmptyValues(),
			Borrower:   *borrower,
			TTL:        *ttl,
			Boot:       !*noBoot,
		})
	}
}

func bindRenew(flags *flag.FlagSet) action {
	ttl := flags.Duration("ttl", 0, "new lease duration measured from now")
	metadata := newMetaFlag()
	flags.Var(metadata, "meta", metadataUsage+"; an empty value removes the key")

	return func(ctx context.Context, environment *env, args []string) (any, error) {
		leaseID, err := requireArg(args, "lease-id")
		if err != nil {
			return nil, err
		}

		return environment.pool.Renew(ctx, pool.RenewRequest{
			LeaseID:  leaseID,
			TTL:      *ttl,
			Metadata: metadata.values,
		})
	}
}

func bindRelease(flags *flag.FlagSet) action {
	shutdown := flags.Bool("shutdown", false, "power the device off after releasing it")

	return func(ctx context.Context, environment *env, args []string) (any, error) {
		leaseID, err := requireArg(args, "lease-id")
		if err != nil {
			return nil, err
		}

		return environment.pool.Release(ctx, leaseID, *shutdown)
	}
}

func bindImport(flags *flag.FlagSet) action {
	undo := flags.Bool("undo", false, "stop the pool from handing this device out")

	return func(ctx context.Context, environment *env, args []string) (any, error) {
		deviceID, err := requireArg(args, "device-id")
		if err != nil {
			return nil, err
		}

		return environment.pool.Import(ctx, deviceID, *undo)
	}
}

func bindReclaim(flags *flag.FlagSet) action {
	force := flags.Bool("force", false, "reclaim even a lease that has not expired")

	return func(ctx context.Context, environment *env, args []string) (any, error) {
		deviceID, err := requireArg(args, "device-id")
		if err != nil {
			return nil, err
		}

		return environment.pool.Reclaim(ctx, deviceID, *force)
	}
}

// parseFlags parses a command's options and returns its positional arguments.
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

func requireNoArgs(args []string) error {
	if len(args) > 0 {
		return errs.New(errs.CodeInvalidArgs, "unexpected argument %q", args[0])
	}

	return nil
}

// metadataUsage documents the free-form lease metadata. Nothing is recorded
// unless the caller asks for it.
const metadataUsage = "lease metadata as `key=value`; repeatable. " +
	"Stored as-is and shown by list"

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
