package pool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pchalupa/simpool/internal/config"
	"github.com/pchalupa/simpool/internal/store"
)

// Check statuses reported by doctor.
const (
	StatusOK   = "ok"
	StatusWarn = "warn"
	StatusFail = "fail"
)

// Check is a single diagnostic result.
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// Counts summarises the pool's contents.
type Counts struct {
	Total      int `json:"total"`
	PoolOwned  int `json:"pool_owned"`
	External   int `json:"external"`
	Borrowable int `json:"borrowable"`
	Missing    int `json:"missing"`
	Leased     int `json:"leased"`
}

// DoctorReport is the full health picture of a host's pool.
type DoctorReport struct {
	Healthy         bool            `json:"healthy"`
	Platform        string          `json:"platform"`
	StateDir        string          `json:"state_dir"`
	Database        string          `json:"database"`
	SchemaVersion   int             `json:"schema_version"`
	ExpectedSchema  int             `json:"expected_schema_version"`
	MaxDevices      int             `json:"max_devices"`
	LeaseTTL        string          `json:"lease_ttl"`
	Devices         Counts          `json:"devices"`
	Checks          []Check         `json:"checks"`
	Reconciliation  ReconcileReport `json:"reconciliation"`
	PendingCreates  int             `json:"pending_creates"`
	ToolchainDetail string          `json:"toolchain,omitempty"`
}

// Doctor reports whether the pool is usable and what reconciliation changed.
func (p *Pool) Doctor(ctx context.Context) (DoctorReport, error) {
	report := DoctorReport{
		Healthy:        true,
		Platform:       p.provider.Platform(),
		StateDir:       p.cfg.StateDir,
		Database:       p.store.Path(),
		ExpectedSchema: store.SchemaVersion,
		MaxDevices:     p.cfg.MaxDevices,
		LeaseTTL:       p.cfg.LeaseTTL.String(),
		Checks:         []Check{},
	}

	toolchain, toolchainErr := p.provider.Preflight(ctx)
	report.ToolchainDetail = toolchain

	switch {
	case toolchainErr != nil:
		report.Healthy = false
		report.Checks = append(report.Checks, Check{
			Name:   "toolchain",
			Status: StatusFail,
			Detail: toolchainErr.Error(),
		})
	default:
		report.Checks = append(report.Checks, Check{
			Name:   "toolchain",
			Status: StatusOK,
			Detail: toolchain,
		})
	}

	report.Checks = append(report.Checks, p.checkStateDir())

	if toolchainErr == nil {
		reconciliation, err := p.Reconcile(ctx)
		if err != nil {
			return DoctorReport{}, err
		}

		report.Reconciliation = reconciliation
	}

	err := p.store.Read(ctx, func(tx *store.Tx) error {
		version, err := tx.AppliedSchemaVersion(ctx)
		if err != nil {
			return err
		}

		report.SchemaVersion = version

		devices, err := tx.ListDevices(ctx)
		if err != nil {
			return err
		}

		leases, err := tx.ListLiveLeases(ctx)
		if err != nil {
			return err
		}

		report.Devices = countDevices(devices, len(leases))

		pending, err := tx.PendingOperations(ctx)
		if err != nil {
			return err
		}

		report.PendingCreates = len(pending)

		return nil
	})
	if err != nil {
		return DoctorReport{}, err
	}

	report.Checks = append(report.Checks, schemaCheck(report))
	report.Checks = append(report.Checks, capacityCheck(report))

	if report.Devices.Missing > 0 {
		report.Checks = append(report.Checks, Check{
			Name:   "devices_present",
			Status: StatusWarn,
			Detail: fmt.Sprintf("%d recorded device(s) no longer exist on this host; they will not be handed out", report.Devices.Missing),
		})
	}

	if report.Devices.Borrowable == 0 {
		report.Checks = append(report.Checks, Check{
			Name:   "borrowable_devices",
			Status: StatusWarn,
			Detail: "no borrowable device; acquire with --create-if-needed or run `simpool import <device-id>`",
		})
	}

	if report.PendingCreates > 0 {
		report.Checks = append(report.Checks, Check{
			Name:   "operations",
			Status: StatusWarn,
			Detail: fmt.Sprintf("%d create operation(s) in flight or awaiting reconciliation", report.PendingCreates),
		})
	}

	for _, check := range report.Checks {
		if check.Status == StatusFail {
			report.Healthy = false
		}
	}

	return report, nil
}

func (p *Pool) checkStateDir() Check {
	probe := filepath.Join(p.cfg.StateDir, ".simpool-write-probe")

	if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
		return Check{
			Name:   "state_dir",
			Status: StatusFail,
			Detail: fmt.Sprintf("%s is not writable: %s", p.cfg.StateDir, err),
		}
	}

	os.Remove(probe)

	detail := p.cfg.StateDir
	if os.Getenv(config.EnvStateDir) != "" {
		detail += " (overridden by " + config.EnvStateDir + ")"
	}

	return Check{
		Name:   "state_dir",
		Status: StatusOK,
		Detail: detail,
	}
}

func schemaCheck(report DoctorReport) Check {
	if report.SchemaVersion == report.ExpectedSchema {
		return Check{
			Name:   "schema",
			Status: StatusOK,
			Detail: fmt.Sprintf("version %d", report.SchemaVersion),
		}
	}

	return Check{
		Name:   "schema",
		Status: StatusFail,
		Detail: fmt.Sprintf("database is at version %d, this build expects %d", report.SchemaVersion, report.ExpectedSchema),
	}
}

func capacityCheck(report DoctorReport) Check {
	status := StatusOK
	if report.Devices.PoolOwned >= report.MaxDevices {
		status = StatusWarn
	}

	return Check{
		Name:   "capacity",
		Status: status,
		Detail: fmt.Sprintf("%d/%d pool-owned devices, %d leased", report.Devices.PoolOwned, report.MaxDevices, report.Devices.Leased),
	}
}

func countDevices(devices []store.Device, leased int) Counts {
	counts := Counts{Leased: leased}

	for _, device := range devices {
		counts.Total++

		// A device that no longer exists is reported on its own; it neither
		// occupies pool capacity nor can be handed out.
		if device.Missing() {
			counts.Missing++

			continue
		}

		switch {
		case device.Managed():
			counts.PoolOwned++
		default:
			counts.External++
		}

		if device.Borrowable {
			counts.Borrowable++
		}
	}

	return counts
}
