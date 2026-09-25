package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/pchalupa/simpool/internal/errs"
	"github.com/pchalupa/simpool/internal/pool"
	"github.com/pchalupa/simpool/internal/store"
)

// response is the JSON envelope every command emits with --json.
type response struct {
	OK      bool           `json:"ok"`
	Command string         `json:"command"`
	Data    any            `json:"data,omitempty"`
	Error   *responseError `json:"error,omitempty"`
}

type responseError struct {
	Code    errs.Code      `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func writeResult(out io.Writer, command string, result any, asJSON bool) error {
	if !asJSON {
		return renderHuman(out, result)
	}

	return writeJSON(out, response{
		OK:      true,
		Command: command,
		Data:    result,
	})
}

// writeError renders a failure. With --json it goes to stdout so an agent reads
// one stream; otherwise it goes to stderr like any CLI error.
func writeError(stdout, stderr io.Writer, command string, err error, asJSON bool) {
	if !asJSON {
		fmt.Fprintf(stderr, "simpool %s: %s (%s)\n", command, err, errs.CodeOf(err))

		if hint, ok := errs.DetailsOf(err)["hint"].(string); ok {
			fmt.Fprintf(stderr, "hint: %s\n", hint)
		}

		return
	}

	writeJSON(stdout, response{
		OK:      false,
		Command: command,
		Error: &responseError{
			Code:    errs.CodeOf(err),
			Message: err.Error(),
			Details: errs.DetailsOf(err),
		},
	})
}

func writeJSON(out io.Writer, payload response) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")

	if err := encoder.Encode(payload); err != nil {
		return errs.Wrap(err, errs.CodeInternal, "encoding JSON response")
	}

	return nil
}

func renderHuman(out io.Writer, result any) error {
	switch value := result.(type) {
	case []pool.DeviceView:
		renderDeviceTable(out, value)
	case pool.DeviceView:
		renderDeviceDetail(out, value)
	case pool.AcquireResult:
		renderAcquire(out, value)
	case pool.RenewResult:
		fmt.Fprintf(out, "renewed lease %s on %s until %s\n", value.Lease.ID, value.Device.ID, localTime(value.Lease.ExpiresAt))
	case pool.ReleaseResult:
		renderRelease(out, value)
	case pool.CreateResult:
		fmt.Fprintf(out, "created %s (%s, %s)\n", value.Device.ID, value.Device.Name, shortRuntime(value.Device.Runtime))
	case pool.DeleteResult:
		fmt.Fprintf(out, "deleted %s\n", value.DeviceID)
	case pool.ImportResult:
		renderImport(out, value)
	case pool.ReclaimResult:
		renderReclaim(out, value)
	case pool.ShutdownResult:
		fmt.Fprintf(out, "shut down %s\n", value.DeviceID)
	case pool.DoctorReport:
		renderDoctor(out, value)
	case nil:
		return nil
	default:
		fmt.Fprintf(out, "%v\n", value)
	}

	return nil
}

func renderDeviceTable(out io.Writer, views []pool.DeviceView) {
	if len(views) == 0 {
		fmt.Fprintln(out, "no devices known to this pool; run `simpool acquire --create-if-needed` or `simpool import <device-id>`")

		return
	}

	writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "DEVICE ID\tNAME\tRUNTIME\tSTATE\tOWNER\tLEASE\tMETADATA")

	for _, view := range views {
		fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			view.Device.ID,
			view.Device.Name,
			shortRuntime(view.Device.Runtime),
			deviceState(view.Device),
			owner(view.Device),
			leaseSummary(view.Lease),
			metadataSummary(view.Lease),
		)
	}

	writer.Flush()
}

func renderDeviceDetail(out io.Writer, view pool.DeviceView) {
	device := view.Device

	fmt.Fprintf(out, "device       %s\n", device.ID)
	fmt.Fprintf(out, "name         %s\n", device.Name)
	fmt.Fprintf(out, "platform     %s\n", device.Platform)
	fmt.Fprintf(out, "device type  %s\n", shortDeviceType(device.DeviceType))
	fmt.Fprintf(out, "runtime      %s\n", shortRuntime(device.Runtime))
	fmt.Fprintf(out, "state        %s\n", deviceState(device))
	fmt.Fprintf(out, "owner        %s\n", owner(device))
	fmt.Fprintf(out, "borrowable   %t\n", device.Borrowable)

	if view.Lease == nil {
		fmt.Fprintln(out, "lease        none")

		return
	}

	fmt.Fprintf(out, "lease        %s\n", view.Lease.ID)
	fmt.Fprintf(out, "borrower     %s\n", view.Lease.Borrower)
	fmt.Fprintf(out, "acquired     %s\n", localTime(view.Lease.AcquiredAt))
	fmt.Fprintf(out, "expires      %s\n", localTime(view.Lease.ExpiresAt))

	if len(view.Lease.Metadata) == 0 {
		fmt.Fprintln(out, "metadata     none")

		return
	}

	for index, key := range metadataKeys(view.Lease.Metadata) {
		label := "metadata"
		if index > 0 {
			label = ""
		}

		fmt.Fprintf(out, "%-12s %s = %s\n", label, key, view.Lease.Metadata[key])
	}
}

func renderAcquire(out io.Writer, result pool.AcquireResult) {
	verb := "leased"
	if result.Created {
		verb = "created and leased"
	}

	fmt.Fprintf(out, "%s %s (%s, %s)\n", verb, result.Device.ID, result.Device.Name, shortRuntime(result.Device.Runtime))
	fmt.Fprintf(out, "lease   %s (expires %s)\n", result.Lease.ID, localTime(result.Lease.ExpiresAt))
	fmt.Fprintf(out, "booted  %t\n", result.Booted)

	for _, key := range metadataKeys(result.Lease.Metadata) {
		fmt.Fprintf(out, "meta    %s = %s\n", key, result.Lease.Metadata[key])
	}
}

func renderRelease(out io.Writer, result pool.ReleaseResult) {
	if result.AlreadyReleased {
		fmt.Fprintf(out, "lease %s was already closed (%s); device %s untouched\n", result.LeaseID, result.ReleaseReason, result.DeviceID)

		return
	}

	suffix := "left booted"
	if result.ShutDown {
		suffix = "shut down"
	}

	fmt.Fprintf(out, "released lease %s; device %s %s\n", result.LeaseID, result.DeviceID, suffix)
}

func renderImport(out io.Writer, result pool.ImportResult) {
	if result.Device.Borrowable {
		fmt.Fprintf(out, "device %s is now borrowable (still owned by %s, simpool will not delete it)\n", result.Device.ID, owner(result.Device))

		return
	}

	fmt.Fprintf(out, "device %s is no longer borrowable\n", result.Device.ID)
}

func renderReclaim(out io.Writer, result pool.ReclaimResult) {
	if result.AlreadyFree {
		fmt.Fprintf(out, "device %s holds no live lease\n", result.DeviceID)

		return
	}

	fmt.Fprintf(out, "reclaimed lease %s from device %s\n", result.ReclaimedLease, result.DeviceID)
}

func renderDoctor(out io.Writer, report pool.DoctorReport) {
	for _, check := range report.Checks {
		fmt.Fprintf(out, "%-5s %-18s %s\n", strings.ToUpper(check.Status), check.Name, check.Detail)
	}

	fmt.Fprintf(
		out,
		"\ndevices    %d total, %d pool-owned, %d external, %d borrowable, %d leased, %d missing\n",
		report.Devices.Total,
		report.Devices.PoolOwned,
		report.Devices.External,
		report.Devices.Borrowable,
		report.Devices.Leased,
		report.Devices.Missing,
	)
	fmt.Fprintf(out, "database   %s (schema %d)\n", report.Database, report.SchemaVersion)
	fmt.Fprintf(out, "lease ttl  %s\n", report.LeaseTTL)

	reconciled := report.Reconciliation

	if len(reconciled.ExpiredLeases)+len(reconciled.MissingDevices)+len(reconciled.AdoptedDevices)+len(reconciled.AbandonedOps) > 0 {
		fmt.Fprintf(
			out,
			"reconciled %d expired lease(s), %d missing device(s), %d adopted device(s), %d abandoned operation(s)\n",
			len(reconciled.ExpiredLeases),
			len(reconciled.MissingDevices),
			len(reconciled.AdoptedDevices),
			len(reconciled.AbandonedOps),
		)
	}
}

func deviceState(device store.Device) string {
	if device.Missing() {
		return "missing"
	}

	if !device.Available {
		return "unavailable"
	}

	return device.State
}

func owner(device store.Device) string {
	if device.Managed() {
		return "pool"
	}

	if device.Borrowable {
		return "external(imported)"
	}

	return "external"
}

func leaseSummary(lease *store.Lease) string {
	if lease == nil {
		return "free"
	}

	return fmt.Sprintf("%s by %s until %s", lease.ID, lease.Borrower, localTime(lease.ExpiresAt))
}

// metadataSummary renders the whole bag on one line, with the keys simpool
// acts on first so a table stays scannable.
func metadataSummary(lease *store.Lease) string {
	if lease == nil || len(lease.Metadata) == 0 {
		return "-"
	}

	pairs := make([]string, 0, len(lease.Metadata))
	for _, key := range metadataKeys(lease.Metadata) {
		pairs = append(pairs, key+"="+lease.Metadata[key])
	}

	return strings.Join(pairs, " ")
}

// metadataKeys orders the well-known keys first, then the rest alphabetically.
func metadataKeys(metadata map[string]string) []string {
	rank := map[string]int{
		store.MetaMetroPort: 0,
		store.MetaWorktree:  1,
	}

	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		keys = append(keys, key)
	}

	sort.Slice(keys, func(i, j int) bool {
		left, leftKnown := rank[keys[i]]
		right, rightKnown := rank[keys[j]]

		if leftKnown != rightKnown {
			return leftKnown
		}

		if leftKnown && left != right {
			return left < right
		}

		return keys[i] < keys[j]
	})

	return keys
}

func orDash(value string) string {
	if value == "" {
		return "-"
	}

	return value
}

func localTime(value time.Time) string {
	return value.Local().Format("2006-01-02 15:04")
}

// shortRuntime trims the CoreSimulator prefix so tables stay readable.
func shortRuntime(identifier string) string {
	trimmed := strings.TrimPrefix(identifier, "com.apple.CoreSimulator.SimRuntime.")

	return strings.ReplaceAll(trimmed, "-", " ")
}

func shortDeviceType(identifier string) string {
	trimmed := strings.TrimPrefix(identifier, "com.apple.CoreSimulator.SimDeviceType.")

	return strings.ReplaceAll(trimmed, "-", " ")
}
