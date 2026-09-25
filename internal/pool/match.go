package pool

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/pchalupa/simpool/internal/errs"
	"github.com/pchalupa/simpool/internal/provider"
)

// resolveDeviceType maps a user-supplied device type — an identifier, a display
// name, or an unambiguous fragment of one — onto a platform identifier.
func (p *Pool) resolveDeviceType(ctx context.Context, input string) (string, error) {
	if input == "" {
		return "", nil
	}

	types, err := p.provider.ListDeviceTypes(ctx)
	if err != nil {
		return "", err
	}

	candidates := make([]string, 0, len(types))
	names := make([]string, 0, len(types))

	for _, deviceType := range types {
		names = append(names, deviceType.Name)

		if deviceType.ID == input || strings.EqualFold(deviceType.Name, input) {
			return deviceType.ID, nil
		}

		if containsFold(deviceType.Name, input) || containsFold(deviceType.ID, input) {
			candidates = append(candidates, deviceType.ID)
		}
	}

	return single(candidates, names, "device type", input)
}

// resolveRuntime maps a user-supplied runtime — an identifier, "iOS 27.0", or
// "27.0" — onto a platform identifier. Unavailable runtimes are rejected.
func (p *Pool) resolveRuntime(ctx context.Context, input string) (string, error) {
	if input == "" {
		return "", nil
	}

	runtimes, err := p.provider.ListRuntimes(ctx)
	if err != nil {
		return "", err
	}

	candidates := []string{}
	names := make([]string, 0, len(runtimes))

	for _, runtime := range runtimes {
		if !runtime.Available {
			continue
		}

		names = append(names, runtime.Name)

		if runtime.ID == input || strings.EqualFold(runtime.Name, input) || runtime.Version == input {
			return runtime.ID, nil
		}

		if containsFold(runtime.Name, input) || containsFold(runtime.ID, input) {
			candidates = append(candidates, runtime.ID)
		}
	}

	return single(candidates, names, "runtime", input)
}

func single(candidates, known []string, kind, input string) (string, error) {
	switch len(candidates) {
	case 1:
		return candidates[0], nil
	case 0:
		return "", errs.New(errs.CodeInvalidArgs, "no installed %s matches %q", kind, input).
			WithDetail("available", known)
	default:
		return "", errs.New(errs.CodeInvalidArgs, "%q matches %d %ss; be more specific", input, len(candidates), kind).
			WithDetail("matches", candidates)
	}
}

func containsFold(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}

// iPhoneName captures the generation and variant of an iPhone device type, used
// to pick a sensible default when the caller does not name one.
var iPhoneName = regexp.MustCompile(`(?i)^iPhone (\d+)(.*)$`)

// defaultDeviceType picks the newest plain iPhone profile available. The rule is
// deterministic so two agents creating devices land on the same model; set
// SIMPOOL_DEFAULT_DEVICE_TYPE to override it.
func (p *Pool) defaultDeviceType(ctx context.Context) (string, error) {
	types, err := p.provider.ListDeviceTypes(ctx)
	if err != nil {
		return "", err
	}

	bestID := ""
	bestScore := [2]int{-1, -1}

	for _, deviceType := range types {
		match := iPhoneName.FindStringSubmatch(deviceType.Name)
		if match == nil {
			continue
		}

		generation, err := strconv.Atoi(match[1])
		if err != nil {
			continue
		}

		score := [2]int{generation, variantRank(match[2])}
		if score[1] < 0 {
			continue
		}

		if score[0] > bestScore[0] || (score[0] == bestScore[0] && score[1] > bestScore[1]) {
			bestScore = score
			bestID = deviceType.ID
		}
	}

	if bestID == "" {
		return "", errs.New(errs.CodeInvalidArgs, "no iPhone device type is installed; pass --device-type").
			WithDetail("hint", "run `xcrun simctl list devicetypes` to see what Xcode installed")
	}

	return bestID, nil
}

// variantRank orders iPhone variants: a plain model is preferred over Pro, and
// oversized or budget variants are never chosen automatically.
func variantRank(suffix string) int {
	switch strings.TrimSpace(strings.ToLower(suffix)) {
	case "":
		return 2
	case "pro":
		return 1
	default:
		return -1
	}
}

// defaultRuntime picks the newest installed runtime. Set
// SIMPOOL_DEFAULT_RUNTIME to override it.
func (p *Pool) defaultRuntime(ctx context.Context) (string, error) {
	runtimes, err := p.provider.ListRuntimes(ctx)
	if err != nil {
		return "", err
	}

	available := []provider.Runtime{}

	for _, runtime := range runtimes {
		if runtime.Available {
			available = append(available, runtime)
		}
	}

	if len(available) == 0 {
		return "", errs.New(errs.CodeInvalidArgs, "no simulator runtime is installed; install one through Xcode")
	}

	sort.Slice(available, func(i, j int) bool {
		return compareVersions(available[i].Version, available[j].Version) > 0
	})

	return available[0].ID, nil
}

// compareVersions compares dotted numeric versions, e.g. "27.0" and "26.5".
func compareVersions(left, right string) int {
	leftParts := strings.Split(left, ".")
	rightParts := strings.Split(right, ".")

	for index := 0; index < len(leftParts) || index < len(rightParts); index++ {
		leftValue := versionPart(leftParts, index)
		rightValue := versionPart(rightParts, index)

		if leftValue != rightValue {
			if leftValue > rightValue {
				return 1
			}

			return -1
		}
	}

	return 0
}

func versionPart(parts []string, index int) int {
	if index >= len(parts) {
		return 0
	}

	value, err := strconv.Atoi(parts[index])
	if err != nil {
		return 0
	}

	return value
}
