package pool

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/pchalupa/simpool/internal/errs"
	"github.com/pchalupa/simpool/internal/store"
)

// Limits on the metadata bag. It is a place for a borrower to record what it is
// doing with a device, not a document store.
const (
	maxMetadataKeys  = 16
	maxMetadataValue = 1024
)

// metadataKey is the shape a key is normalised to: `--meta metro-port=8081`
// and `--meta metro_port=8081` record the same key.
var metadataKey = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// normalizeMetadata validates a metadata bag and canonicalises the keys simpool
// itself acts on. Every key is optional, including the well-known ones: a lease
// with no worktree and no Metro server carries an empty bag.
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

	if err := normalizeWellKnown(normalized); err != nil {
		return nil, err
	}

	return normalized, nil
}

// normalizeWellKnown validates the two keys simpool reads. Both stay absent
// unless the borrower set them, and an empty value is a removal rather than a
// value to validate.
func normalizeWellKnown(metadata map[string]string) error {
	if worktree := metadata[store.MetaWorktree]; worktree != "" {
		canonical, err := canonicalWorktree(worktree)
		if err != nil {
			return err
		}

		metadata[store.MetaWorktree] = canonical
	}

	port := metadata[store.MetaMetroPort]
	if port == "" {
		return nil
	}

	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return errs.New(errs.CodeInvalidArgs, "metadata %s must be a port between 1 and 65535, got %q", store.MetaMetroPort, port)
	}

	// Store the canonical spelling so two borrowers cannot claim "8081" and
	// "08081" as different ports.
	metadata[store.MetaMetroPort] = strconv.Itoa(number)

	return nil
}

func normalizeKey(key string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(key)), "-", "_")
}

// mergeMetadata applies an update to an existing bag. A key given with an empty
// value is removed, which is how a borrower drops a Metro port it no longer
// serves.
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
