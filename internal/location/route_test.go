package location

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoute_Locate(t *testing.T) {
	startedAt := time.Unix(0, 0)
	waypoints := []Coordinates{{}, {Longitude: 1}, {Latitude: 1, Longitude: 1}}
	const kmPerDegree = earthRadiusKM * radiansPerDegree
	route := NewRoute(waypoints, kmPerDegree, "car-0001", startedAt)
	first, ok := route.Locate(startedAt)
	require.True(t, ok)
	require.Positive(t, first.Longitude)
	require.Less(t, first.Longitude, 0.01)
	assert.Zero(t, first.Latitude)

	tests := []struct {
		name    string
		elapsed time.Duration
		want    Coordinates
	}{
		{name: "before start", elapsed: -time.Minute, want: first},
		{name: "first segment", elapsed: 30 * time.Minute, want: Coordinates{Longitude: first.Longitude + 0.5}},
		{name: "at waypoint", elapsed: time.Duration((1 - first.Longitude) * float64(time.Hour)), want: waypoints[1]},
		{name: "second segment", elapsed: 90 * time.Minute, want: Coordinates{Latitude: first.Longitude + 0.5, Longitude: 1}},
		{name: "after arrival", elapsed: 3 * time.Hour, want: waypoints[2]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := route.Locate(startedAt.Add(tt.elapsed))
			require.True(t, ok)
			assert.InDelta(t, tt.want.Latitude, got.Latitude, 1e-10)
			assert.InDelta(t, tt.want.Longitude, got.Longitude, 1e-10)
		})
	}
	same, _ := NewRoute(waypoints, kmPerDegree, "car-0001", startedAt).Locate(startedAt)
	other, _ := NewRoute(waypoints, kmPerDegree, "car-0002", startedAt).Locate(startedAt)
	assert.Equal(t, first, same)
	assert.NotEqual(t, first, other)
}

func TestRoute_LocateAcrossAntimeridian(t *testing.T) {
	startedAt := time.Unix(0, 0)
	from, to := Coordinates{Longitude: 179}, Coordinates{Longitude: -179}
	const kmPerDegree = earthRadiusKM * radiansPerDegree
	route := NewRoute([]Coordinates{from, from, to}, kmPerDegree, "car-0001", startedAt)
	before, _ := route.Locate(startedAt.Add(59 * time.Minute))
	after, _ := route.Locate(startedAt.Add(61 * time.Minute))
	assert.Greater(t, before.Longitude, 179.0)
	assert.Less(t, after.Longitude, -179.0)
	assert.LessOrEqual(t, math.Abs(before.Longitude), 180.0)
	assert.LessOrEqual(t, math.Abs(after.Longitude), 180.0)
}
