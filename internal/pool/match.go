package pool

import (
	"context"
	"strings"

	"github.com/pchalupa/simpool/internal/errs"
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
