// Package config resolves simpool's host-local configuration: where the
// authoritative state lives and lease defaults.
package config

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/pchalupa/simpool/internal/errs"
)

// Environment variables recognised by simpool.
const (
	EnvStateDir        = "SIMPOOL_STATE_DIR"
	EnvLeaseTTL        = "SIMPOOL_LEASE_TTL"
	EnvBorrower        = "SIMPOOL_BORROWER"
	EnvOpTimeout       = "SIMPOOL_OP_TIMEOUT"
	EnvBorrowUnmanaged = "SIMPOOL_BORROW_UNMANAGED"
)

// Defaults applied when the corresponding environment variable is unset.
const (
	DefaultLeaseTTL  = 2 * time.Hour
	DefaultOpTimeout = 5 * time.Minute
)

// Config is the fully resolved configuration for a single CLI invocation.
type Config struct {
	StateDir     string
	DatabasePath string
	LeaseTTL     time.Duration
	OpTimeout    time.Duration
	Borrower     string
	// BorrowUnmanaged makes simulators borrowable without an explicit
	// `simpool import`. Off by default so simpool never hands out a user's
	// personal simulator.
	BorrowUnmanaged bool
}

// Load resolves configuration from the environment.
func Load() (Config, error) {
	stateDir, err := stateDir()
	if err != nil {
		return Config{}, err
	}

	leaseTTL, err := durationFromEnv(EnvLeaseTTL, DefaultLeaseTTL)
	if err != nil {
		return Config{}, err
	}

	opTimeout, err := durationFromEnv(EnvOpTimeout, DefaultOpTimeout)
	if err != nil {
		return Config{}, err
	}

	return Config{
		StateDir:        stateDir,
		DatabasePath:    filepath.Join(stateDir, "pool.db"),
		LeaseTTL:        leaseTTL,
		OpTimeout:       opTimeout,
		Borrower:        defaultBorrower(),
		BorrowUnmanaged: boolFromEnv(EnvBorrowUnmanaged),
	}, nil
}

// stateDir returns the authoritative state directory. It is never a cache or
// OS temp directory by default: the database outlives the devices it tracks.
func stateDir() (string, error) {
	if override := os.Getenv(EnvStateDir); override != "" {
		abs, err := filepath.Abs(override)
		if err != nil {
			return "", errs.Wrap(err, errs.CodeInvalidArgs, "resolving %s=%q", EnvStateDir, override)
		}

		return abs, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", errs.Wrap(err, errs.CodeState, "resolving home directory")
	}

	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "simpool"), nil
	default:
		if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
			return filepath.Join(xdg, "simpool"), nil
		}

		return filepath.Join(home, ".local", "state", "simpool"), nil
	}
}

// defaultBorrower identifies the caller when `--borrower` is not given.
func defaultBorrower() string {
	if borrower := os.Getenv(EnvBorrower); borrower != "" {
		return borrower
	}

	name := "unknown"
	if current, err := user.Current(); err == nil && current.Username != "" {
		name = current.Username
	}

	host, err := os.Hostname()
	if err != nil {
		host = "localhost"
	}

	return fmt.Sprintf("%s@%s/%d", name, host, os.Getpid())
}

func durationFromEnv(key string, fallback time.Duration) (time.Duration, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}

	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return 0, errs.New(errs.CodeInvalidArgs, "%s must be a positive Go duration (e.g. 90m), got %q", key, raw)
	}

	return value, nil
}

func boolFromEnv(key string) bool {
	value, err := strconv.ParseBool(os.Getenv(key))

	return err == nil && value
}
