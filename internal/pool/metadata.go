package pool

import (
	"regexp"
	"strings"

	"github.com/pchalupa/simpool/internal/errs"
)

// Limits on the metadata bag. It is a place for a borrower to record what it is
// doing with a device, not a document store.
const (
	maxMetadataKeys  = 16
	maxMetadataValue = 1024
)

// metadataKey is the shape a key is normalised to: `--meta my-key=value` and
// `--meta my_key=value` record the same key.
var metadataKey = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// normalizeMetadata validates a metadata bag. simpool gives no key a special
// meaning: values are stored exactly as given.
func normalizeMetadata(metadata map[string]string) (map[string]string, error) {
	if len(metadata) == 0 {
		return map[string]string{}, nil
	}

	if len(metadata) > maxMetadataKeys {
		return nil, errs.New(errs.CodeInvalidArgs, "a lease carries at most %d metadata keys, got %d", maxMetadataKeys, len(metadata))
	}

	normalized := make(map[string]string, len(metadata))

	for rawKey, value := range metadata {
		key := normalizeKey(rawKey)

		if !metadataKey.MatchString(key) {
			return nil, errs.New(errs.CodeInvalidArgs, "metadata key %q must be letters, digits or underscores", rawKey)
		}

		if len(value) > maxMetadataValue {
			return nil, errs.New(errs.CodeInvalidArgs, "metadata value for %q is longer than %d characters", key, maxMetadataValue)
		}

		if _, duplicate := normalized[key]; duplicate {
			return nil, errs.New(errs.CodeInvalidArgs, "metadata key %q is given twice", key)
		}

		normalized[key] = value
	}

	return normalized, nil
}

func normalizeKey(key string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(key)), "-", "_")
}

// mergeMetadata applies an update to an existing bag. A key given with an empty
// value is removed.
func mergeMetadata(current, update map[string]string) map[string]string {
	merged := make(map[string]string, len(current)+len(update))

	for key, value := range current {
		merged[key] = value
	}

	for key, value := range update {
		if value == "" {
			delete(merged, key)

			continue
		}

		merged[key] = value
	}

	return merged
}
