package machine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		name    string
		target  string
		current string
		want    int
		wantErr string
	}{
		{name: "numeric order", target: "1.0.10", current: "1.0.9", want: 1},
		{name: "major version", target: "2.0", current: "1.9.9", want: 1},
		{name: "missing segment", target: "1.0.1", current: "1.0", want: 1},
		{name: "lower target", target: "1.0", current: "1.0.0.1", want: -1},
		{name: "trailing zeros", target: "1.0", current: "1.0.0"},
		{name: "all zeros", target: "0", current: "0.0.0"},
		{name: "leading zeros", target: "000000001.01", current: "1.1"},
		{name: "maximum segment", target: "1.999999999", current: "1.999999998", want: 1},
		{name: "empty target", target: "", current: "1.0", wantErr: "parse target version"},
		{name: "prerelease", target: "1.0-beta", current: "1.0", wantErr: "parse target version"},
		{name: "prefix", target: "v1.0", current: "1.0", wantErr: "parse target version"},
		{name: "empty segment", target: "1..0", current: "1.0", wantErr: "parse target version"},
		{name: "trailing separator", target: "1.0.", current: "1.0", wantErr: "parse target version"},
		{name: "leading separator", target: ".1", current: "1.0", wantErr: "parse target version"},
		{name: "negative segment", target: "-1.0", current: "1.0", wantErr: "parse target version"},
		{name: "too many digits", target: "1.0000000000", current: "1.0", wantErr: "parse target version"},
		{name: "non-ASCII digit", target: "１.0", current: "1.0", wantErr: "parse target version"},
		{name: "whitespace", target: "1.0\n", current: "1.0", wantErr: "parse target version"},
		{name: "invalid current", target: "1.0", current: "v1.0", wantErr: "parse current version"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := compareVersions(tt.target, tt.current)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
