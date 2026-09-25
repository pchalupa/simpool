// Package fake is an in-memory provider used by tests to exercise pool
// behaviour without Xcode. It is part of the provider boundary's contract: if a
// pool rule cannot be tested here, it leaked into a platform implementation.
package fake

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/pchalupa/simpool/internal/errs"
	"github.com/pchalupa/simpool/internal/provider"
)

// Provider is a scriptable stand-in for a platform toolchain.
type Provider struct {
	mu       sync.Mutex
	devices  map[string]provider.Device
	types    []provider.DeviceType
	runtimes []provider.Runtime
	counter  int

	// CreateErr, BootErr and DeleteErr make the next matching call fail.
	CreateErr error
	BootErr   error
	DeleteErr error
	// AfterCreate runs once the device exists on the platform but before the
	// caller learns about it, which simulates a CLI process killed mid-create.
	AfterCreate func(device provider.Device) error
}

// New builds a provider with two runtimes and two device types installed.
func New() *Provider {
	return &Provider{
		devices: map[string]provider.Device{},
		types: []provider.DeviceType{
			{ID: "type.iPhone-17", Name: "iPhone 17"},
			{ID: "type.iPhone-17-Pro", Name: "iPhone 17 Pro"},
		},
		runtimes: []provider.Runtime{
			{ID: "runtime.iOS-26-5", Name: "iOS 26.5", Version: "26.5", Available: true},
			{ID: "runtime.iOS-27-0", Name: "iOS 27.0", Version: "27.0", Available: true},
		},
	}
}

// AddExisting registers a device that was created outside simpool.
func (p *Provider) AddExisting(name string) provider.Device {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.insert(name, "type.iPhone-17", "runtime.iOS-27-0")
}

// Remove deletes a device behind simpool's back, as a user would.
func (p *Provider) Remove(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	delete(p.devices, id)
}

// StateOf reports the current state of a device.
func (p *Provider) StateOf(id string) provider.State {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.devices[id].State
}

// Count reports how many devices exist on the fake platform.
func (p *Provider) Count() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return len(p.devices)
}

func (p *Provider) insert(name, deviceType, runtime string) provider.Device {
	p.counter++

	device := provider.Device{
		ID:         fmt.Sprintf("DEV-%04d", p.counter),
		Platform:   provider.PlatformIOS,
		Name:       name,
		DeviceType: deviceType,
		Runtime:    runtime,
		State:      provider.StateShutdown,
		Available:  true,
	}

	p.devices[device.ID] = device

	return device
}

func (p *Provider) Platform() string {
	return provider.PlatformIOS
}

func (p *Provider) ListDevices(context.Context) ([]provider.Device, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	devices := make([]provider.Device, 0, len(p.devices))
	for _, device := range p.devices {
		devices = append(devices, device)
	}

	return devices, nil
}

func (p *Provider) ListDeviceTypes(context.Context) ([]provider.DeviceType, error) {
	return p.types, nil
}

func (p *Provider) ListRuntimes(context.Context) ([]provider.Runtime, error) {
	return p.runtimes, nil
}

func (p *Provider) Inspect(_ context.Context, id string) (provider.Device, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	device, ok := p.devices[id]
	if !ok {
		return provider.Device{}, errs.New(errs.CodeDeviceNotFound, "fake provider has no device %q", id)
	}

	return device, nil
}

func (p *Provider) Create(_ context.Context, request provider.CreateRequest) (provider.Device, error) {
	p.mu.Lock()

	if p.CreateErr != nil {
		err := p.CreateErr
		p.CreateErr = nil
		p.mu.Unlock()

		return provider.Device{}, err
	}

	device := p.insert(request.Name, request.DeviceType, request.Runtime)
	hook := p.AfterCreate
	p.mu.Unlock()

	if hook != nil {
		if err := hook(device); err != nil {
			return provider.Device{}, err
		}
	}

	return device, nil
}

func (p *Provider) Boot(_ context.Context, id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.BootErr != nil {
		err := p.BootErr
		p.BootErr = nil

		return err
	}

	return p.setState(id, provider.StateBooted)
}

func (p *Provider) WaitReady(_ context.Context, id string, _ time.Duration) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, ok := p.devices[id]; !ok {
		return errs.New(errs.CodeDeviceNotFound, "fake provider has no device %q", id)
	}

	return nil
}

func (p *Provider) Shutdown(_ context.Context, id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.setState(id, provider.StateShutdown)
}

func (p *Provider) Delete(_ context.Context, id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.DeleteErr != nil {
		err := p.DeleteErr
		p.DeleteErr = nil

		return err
	}

	if _, ok := p.devices[id]; !ok {
		return errs.New(errs.CodeDeviceNotFound, "fake provider has no device %q", id)
	}

	delete(p.devices, id)

	return nil
}

func (p *Provider) setState(id string, state provider.State) error {
	device, ok := p.devices[id]
	if !ok {
		return errs.New(errs.CodeDeviceNotFound, "fake provider has no device %q", id)
	}

	device.State = state
	p.devices[id] = device

	return nil
}
