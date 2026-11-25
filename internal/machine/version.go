package machine

import (
	"fmt"
	"slices"
	"strings"
)

// compareVersions uses the numeric segments accepted by the server's DottedVersion.
func compareVersions(target, current string) (int, error) {
	targetSegments, err := parseVersion(target)
	if err != nil {
		return 0, fmt.Errorf("parse target version: %w", err)
	}
	currentSegments, err := parseVersion(current)
	if err != nil {
		return 0, fmt.Errorf("parse current version: %w", err)
	}
	return slices.Compare(targetSegments, currentSegments), nil
}

func parseVersion(version string) ([]int, error) {
	parts := strings.Split(version, ".")
	segments := make([]int, 0, len(parts))
	for _, part := range parts {
		if len(part) == 0 || len(part) > 9 {
			return nil, fmt.Errorf("invalid dotted version %q", version)
		}
		segment := 0
		for _, digit := range []byte(part) {
			if digit < '0' || digit > '9' {
				return nil, fmt.Errorf("invalid dotted version %q", version)
			}
			segment = segment*10 + int(digit-'0')
		}
		segments = append(segments, segment)
	}

	// The server treats missing segments as zero.
	for len(segments) > 1 && segments[len(segments)-1] == 0 {
		segments = segments[:len(segments)-1]
	}
	return segments, nil
}
