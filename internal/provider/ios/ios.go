// Package ios implements the simpool provider for iOS simulators on top of
// `xcrun simctl`. Nothing outside this package parses simctl output.
package ios

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/pchalupa/simpool/internal/errs"
	"github.com/pchalupa/simpool/internal/provider"
)

// runtimePrefix identifies the iOS runtimes this provider manages; watchOS,
// tvOS and visionOS devices are deliberately ignored.
const runtimePrefix = "com.apple.CoreSimulator.SimRuntime.iOS-"

// Provider allocates iOS simulators through simctl.
type Provider struct {
	// timeout bounds every simctl invocation that is not explicitly bounded by
	// its own deadline.
	timeout time.Duration
}

// New builds an iOS provider whose simctl calls are bounded by timeout.
func New(timeout time.Duration) *Provider {
	return &Provider{timeout: timeout}
}

func (p *Provider) Platform() string {
	return provider.PlatformIOS
}

type deviceList struct {
	Devices map[string][]struct {
		UDID                 string `json:"udid"`
		Name                 string `json:"name"`
		State                string `json:"state"`
		IsAvailable          bool   `json:"isAvailable"`
		DeviceTypeIdentifier string `json:"deviceTypeIdentifier"`
	} `json:"devices"`
}

func (p *Provider) ListDevices(ctx context.Context) ([]provider.Device, error) {
	out, err := p.simctl(ctx, p.timeout, "list", "-j", "devices")
	if err != nil {
		return nil, err
	}

	var parsed deviceList
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return nil, errs.Wrap(err, errs.CodeProvider, "parsing simctl device list")
	}

	devices := []provider.Device{}

	for runtimeID, entries := range parsed.Devices {
		if !strings.HasPrefix(runtimeID, runtimePrefix) {
			continue
		}

		for _, entry := range entries {
			devices = append(devices, provider.Device{
				ID:         entry.UDID,
				Platform:   provider.PlatformIOS,
				Name:       entry.Name,
				DeviceType: entry.DeviceTypeIdentifier,
				Runtime:    runtimeID,
				State:      normalizeState(entry.State),
				Available:  entry.IsAvailable,
			})
		}
	}

	return devices, nil
}

type deviceTypeList struct {
	DeviceTypes []struct {
		Identifier    string `json:"identifier"`
		Name          string `json:"name"`
		ProductFamily string `json:"productFamily"`
	} `json:"devicetypes"`
}

func (p *Provider) ListDeviceTypes(ctx context.Context) ([]provider.DeviceType, error) {
	out, err := p.simctl(ctx, p.timeout, "list", "-j", "devicetypes")
	if err != nil {
		return nil, err
	}

	var parsed deviceTypeList
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return nil, errs.Wrap(err, errs.CodeProvider, "parsing simctl device type list")
	}

	types := make([]provider.DeviceType, 0, len(parsed.DeviceTypes))

	for _, entry := range parsed.DeviceTypes {
		types = append(types, provider.DeviceType{
			ID:   entry.Identifier,
			Name: entry.Name,
		})
	}

	return types, nil
}

type runtimeList struct {
	Runtimes []struct {
		Identifier  string `json:"identifier"`
		Name        string `json:"name"`
		Version     string `json:"version"`
		Platform    string `json:"platform"`
		IsAvailable bool   `json:"isAvailable"`
	} `json:"runtimes"`
}

func (p *Provider) ListRuntimes(ctx context.Context) ([]provider.Runtime, error) {
	out, err := p.simctl(ctx, p.timeout, "list", "-j", "runtimes")
	if err != nil {
		return nil, err
	}

	var parsed runtimeList
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return nil, errs.Wrap(err, errs.CodeProvider, "parsing simctl runtime list")
	}

	runtimes := []provider.Runtime{}

	for _, entry := range parsed.Runtimes {
		if !strings.HasPrefix(entry.Identifier, runtimePrefix) {
			continue
		}

		runtimes = append(runtimes, provider.Runtime{
			ID:        entry.Identifier,
			Name:      entry.Name,
			Version:   entry.Version,
			Available: entry.IsAvailable,
		})
	}

	return runtimes, nil
}

func (p *Provider) Inspect(ctx context.Context, id string) (provider.Device, error) {
	devices, err := p.ListDevices(ctx)
	if err != nil {
		return provider.Device{}, err
	}

	for _, device := range devices {
		if device.ID == id {
			return device, nil
		}
	}

	return provider.Device{}, errs.New(errs.CodeDeviceNotFound, "simctl does not know device %q", id)
}

func (p *Provider) Create(ctx context.Context, req provider.CreateRequest) (provider.Device, error) {
	out, err := p.simctl(ctx, p.timeout, "create", req.Name, req.DeviceType, req.Runtime)
	if err != nil {
		return provider.Device{}, err
	}

	udid := strings.TrimSpace(out)
	if udid == "" {
		return provider.Device{}, errs.New(errs.CodeProvider, "simctl create returned no device identifier")
	}

	return provider.Device{
		ID:         udid,
		Platform:   provider.PlatformIOS,
		Name:       req.Name,
		DeviceType: req.DeviceType,
		Runtime:    req.Runtime,
		State:      provider.StateShutdown,
		Available:  true,
	}, nil
}

func (p *Provider) Boot(ctx context.Context, id string) error {
	_, err := p.simctl(ctx, p.timeout, "boot", id)
	if err != nil && isStateNoop(err) {
		return nil
	}

	return err
}

func (p *Provider) WaitReady(ctx context.Context, id string, timeout time.Duration) error {
	_, err := p.simctl(ctx, timeout, "bootstatus", id)

	return err
}

func (p *Provider) Shutdown(ctx context.Context, id string) error {
	_, err := p.simctl(ctx, p.timeout, "shutdown", id)
	if err != nil && isStateNoop(err) {
		return nil
	}

	return err
}

func (p *Provider) Delete(ctx context.Context, id string) error {
	_, err := p.simctl(ctx, p.timeout, "delete", id)

	return err
}

func normalizeState(raw string) provider.State {
	switch strings.ToLower(raw) {
	case "booted":
		return provider.StateBooted
	case "booting":
		return provider.StateBooting
	case "shutdown", "shutting down":
		return provider.StateShutdown
	default:
		return provider.StateUnknown
	}
}

// isStateNoop reports whether simctl refused an operation because the device is
// already in the requested state, which simpool treats as success.
func isStateNoop(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "current state")
}

func (p *Provider) simctl(ctx context.Context, timeout time.Duration, args ...string) (string, error) {
	return p.run(ctx, timeout, "xcrun", append([]string{"simctl"}, args...)...)
}

// run executes a bounded external command and maps its failure onto a coded
// simpool error so callers never parse tool output themselves.
func (p *Provider) run(ctx context.Context, timeout time.Duration, name string, args ...string) (string, error) {
	if timeout <= 0 {
		timeout = p.timeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var stdout, stderr bytes.Buffer

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err == nil {
		return stdout.String(), nil
	}

	commandLine := fmt.Sprintf("%s %s", name, strings.Join(args, " "))

	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "", errs.New(errs.CodeTimeout, "%s timed out after %s", commandLine, timeout).
			WithDetail("command", commandLine)
	}

	message := strings.TrimSpace(stderr.String())
	if message == "" {
		message = strings.TrimSpace(stdout.String())
	}

	if message == "" {
		message = err.Error()
	}

	return "", errs.New(errs.CodeProvider, "%s failed: %s", commandLine, message).
		WithDetail("command", commandLine)
}
