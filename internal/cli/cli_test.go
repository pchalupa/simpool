package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pchalupa/simpool/internal/cli"
	"github.com/pchalupa/simpool/internal/config"
	"github.com/pchalupa/simpool/internal/errs"
)

type envelope struct {
	OK      bool   `json:"ok"`
	Command string `json:"command"`
	Error   *struct {
		Code    errs.Code `json:"code"`
		Message string    `json:"message"`
	} `json:"error"`
}

func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()

	var stdout, stderr bytes.Buffer
	status := cli.Run(context.Background(), args, &stdout, &stderr)

	return status, stdout.String(), stderr.String()
}

func requireErrorEnvelope(t *testing.T, stdout, command string, code errs.Code) {
	t.Helper()

	var response envelope
	if err := json.Unmarshal([]byte(stdout), &response); err != nil {
		t.Fatalf("stdout is not a JSON envelope: %v\n%s", err, stdout)
	}

	if response.OK || response.Command != command || response.Error == nil {
		t.Fatalf("unexpected envelope: %s", stdout)
	}

	if response.Error.Code != code {
		t.Fatalf("error code %q, expected %q", response.Error.Code, code)
	}
}

func TestJSONErrorWhenConfigFailsToLoad(t *testing.T) {
	t.Setenv(config.EnvStateDir, t.TempDir())
	t.Setenv(config.EnvMaxDevices, "nope")

	for _, args := range [][]string{
		{"acquire"},
		{"renew", "lease-1"},
		{"renew", "--ttl", "1h", "lease-1"},
	} {
		status, stdout, stderr := run(t, args...)

		if status != errs.ExitInvalidArgs {
			t.Errorf("%v: exit %d, expected %d", args, status, errs.ExitInvalidArgs)
		}

		if stderr != "" {
			t.Errorf("%v: unexpected stderr %q", args, stderr)
		}

		requireErrorEnvelope(t, stdout, args[0], errs.CodeInvalidArgs)

		if !strings.Contains(stdout, config.EnvMaxDevices) {
			t.Errorf("%v: error does not mention %s: %q", args, config.EnvMaxDevices, stdout)
		}
	}
}

func TestJSONErrorWhenDatabaseFailsToOpen(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv(config.EnvStateDir, filepath.Join(blocker, "state"))

	status, stdout, stderr := run(t, "acquire")

	if status != errs.ExitInternal {
		t.Errorf("exit %d, expected %d", status, errs.ExitInternal)
	}

	if stderr != "" {
		t.Errorf("unexpected stderr %q", stderr)
	}

	requireErrorEnvelope(t, stdout, "acquire", errs.CodeState)
}

func TestJSONErrorForBadInvocations(t *testing.T) {
	t.Setenv(config.EnvStateDir, t.TempDir())

	for _, testCase := range []struct {
		args    []string
		command string
	}{
		{args: nil, command: ""},
		{args: []string{"frobnicate"}, command: "frobnicate"},
		{args: []string{"acquire", "--no-such-flag"}, command: "acquire"},
		{args: []string{"acquire", "--json"}, command: "acquire"},
		{args: []string{"renew"}, command: "renew"},
	} {
		status, stdout, stderr := run(t, testCase.args...)

		if status != errs.ExitInvalidArgs {
			t.Errorf("%v: exit %d, expected %d", testCase.args, status, errs.ExitInvalidArgs)
		}

		if stderr != "" {
			t.Errorf("%v: unexpected stderr %q", testCase.args, stderr)
		}

		requireErrorEnvelope(t, stdout, testCase.command, errs.CodeInvalidArgs)
	}
}

func TestHelpDoesNotNeedConfig(t *testing.T) {
	t.Setenv(config.EnvMaxDevices, "nope")

	status, _, stderr := run(t, "acquire", "--help")

	if status != errs.ExitOK {
		t.Errorf("exit %d, expected %d", status, errs.ExitOK)
	}

	if !strings.Contains(stderr, "Usage: simpool acquire") {
		t.Errorf("stderr does not show usage: %q", stderr)
	}
}
