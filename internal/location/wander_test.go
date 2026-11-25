package location

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWander_Locate(t *testing.T) {
	startedAt := time.Unix(0, 0)
	center := Coordinates{Latitude: 37.56, Longitude: 126.97}
	wander := NewWander(center, 1, "car-0001", startedAt)
	first, ok := wander.Locate(startedAt)
	require.True(t, ok)
	beforeStart, _ := wander.Locate(startedAt.Add(-time.Minute))
	assert.Equal(t, first, beforeStart)
	later, _ := wander.Locate(startedAt.Add(time.Minute))
	assert.NotEqual(t, first, later)
	assert.InDelta(t, wanderSpeedKMH/60, distanceKM(first, later), 0.001)

	same, _ := NewWander(center, 1, "car-0001", startedAt).Locate(startedAt.Add(time.Minute))
	other, _ := NewWander(center, 1, "car-0002", startedAt).Locate(startedAt.Add(time.Minute))
	assert.Equal(t, later, same)
	assert.NotEqual(t, later, other)
	assert.NotEqual(t, distanceKM(center, later), distanceKM(center, other))
}

func TestWander_LocateStaysWithinRadius(t *testing.T) {
	tests := []struct {
		name   string
		center Coordinates
	}{
		{name: "mid-latitude", center: Coordinates{Latitude: 37.56, Longitude: 126.97}},
		{name: "antimeridian", center: Coordinates{Longitude: 179.999}},
		{name: "north pole", center: Coordinates{Latitude: 90, Longitude: 180}},
		{name: "south pole", center: Coordinates{Latitude: -90, Longitude: -180}},
	}
	const radiusKM = 2
	startedAt := time.Unix(0, 0)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wander := NewWander(tt.center, radiusKM, "car-0001", startedAt)
			for minute := range 90 {
				got, ok := wander.Locate(startedAt.Add(time.Duration(minute) * time.Minute))
				require.True(t, ok)
				assert.LessOrEqual(t, math.Abs(got.Latitude), 90.0)
				assert.LessOrEqual(t, math.Abs(got.Longitude), 180.0)
				assert.LessOrEqual(t, distanceKM(tt.center, got), float64(radiusKM))
			}
		})
	}
}
