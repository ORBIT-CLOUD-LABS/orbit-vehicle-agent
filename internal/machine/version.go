package machine

import (
	"fmt"
	"slices"
	"strings"
)

// compareVersions는 서버의 DottedVersion과 같은 규칙으로 버전의 숫자 자리를 비교한다.
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

	// 서버는 뒤에 생략된 자리를 0으로 본다.
	for len(segments) > 1 && segments[len(segments)-1] == 0 {
		segments = segments[:len(segments)-1]
	}
	return segments, nil
}
