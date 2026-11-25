package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/config"
	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/location"
)

func validEnv() map[string]string {
	return map[string]string{
		"VEHICLE_ID":       "car-0001",
		"VEHICLE_MODEL":    "model-x",
		"HW_VERSION":       "hw-2",
		"REGION":           "kr",
		"ENROLLMENT_KEY":   "enrollment-secret",
		"OTA_SERVER_URL":   "https://ota.orbit.internal",
		"CHECKIN_INTERVAL": "30",
		"DATA_DIR":         "/data",
		"INITIAL_VERSION":  "1.0.0",
	}
}

func getenvFunc(env map[string]string) func(string) string {
	return func(key string) string {
		return env[key]
	}
}

// VEHICLE-REQ-002
func TestLoad_Valid(t *testing.T) {
	cfg, err := config.Load(getenvFunc(validEnv()))

	require.NoError(t, err)
	assert.Equal(t, "car-0001", cfg.VehicleID)
	assert.Equal(t, "model-x", cfg.VehicleModel)
	assert.Equal(t, "hw-2", cfg.HWVersion)
	assert.Equal(t, "kr", cfg.Region)
	assert.Equal(t, "enrollment-secret", cfg.EnrollmentKey)
	assert.Equal(t, "https://ota.orbit.internal", cfg.OTAServerURL)
	assert.Equal(t, 30*time.Second, cfg.CheckInInterval)
	assert.Equal(t, "/data", cfg.DataDir)
	assert.Equal(t, "1.0.0", cfg.InitialVersion)
}

// VEHICLE-REQ-002
func TestLoad_MissingRequiredValue(t *testing.T) {
	keys := []string{
		"VEHICLE_ID",
		"VEHICLE_MODEL",
		"HW_VERSION",
		"REGION",
		"ENROLLMENT_KEY",
		"OTA_SERVER_URL",
		"CHECKIN_INTERVAL",
		"DATA_DIR",
		"INITIAL_VERSION",
	}

	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			env := validEnv()
			delete(env, key)

			_, err := config.Load(getenvFunc(env))

			require.Error(t, err)
		})
	}
}

// VEHICLE-REQ-002
func TestLoad_InvalidVehicleID(t *testing.T) {
	tests := []struct {
		name      string
		vehicleID string
	}{
		{name: "공백 문자 포함", vehicleID: "car 0001"},
		{name: "허용되지 않은 특수문자 포함", vehicleID: "car_0001"},
		{name: "65자 초과", vehicleID: generateString('a', 65)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnv()
			env["VEHICLE_ID"] = tt.vehicleID

			_, err := config.Load(getenvFunc(env))

			require.Error(t, err)
		})
	}
}

// VEHICLE-REQ-002
func TestLoad_VehicleIDBoundary(t *testing.T) {
	env := validEnv()
	env["VEHICLE_ID"] = generateString('a', 64)

	_, err := config.Load(getenvFunc(env))

	require.NoError(t, err)
}

// VEHICLE-REQ-002
func TestLoad_InvalidOTAServerURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{name: "URL 아님", url: "not-a-url"},
		{name: "scheme 없음", url: "ota.orbit.internal"},
		{name: "허용되지 않은 scheme", url: "ftp://ota.orbit.internal"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnv()
			env["OTA_SERVER_URL"] = tt.url

			_, err := config.Load(getenvFunc(env))

			require.Error(t, err)
		})
	}
}

// VEHICLE-REQ-002
func TestLoad_InvalidCheckInInterval(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "숫자 아님", value: "abc"},
		{name: "0", value: "0"},
		{name: "음수", value: "-5"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnv()
			env["CHECKIN_INTERVAL"] = tt.value

			_, err := config.Load(getenvFunc(env))

			require.Error(t, err)
		})
	}
}

func TestLoad_Location(t *testing.T) {
	tests := []struct {
		name      string
		latitude  string
		longitude string
		want      *location.Coordinates
		wantError string
	}{
		{name: "미설정"},
		{name: "고정 위치", latitude: "37.5665", longitude: "126.978", want: &location.Coordinates{Latitude: 37.5665, Longitude: 126.978}},
		{name: "좌표가 모두 0", latitude: "0", longitude: "0", want: &location.Coordinates{}},
		{name: "최솟값", latitude: "-90", longitude: "-180", want: &location.Coordinates{Latitude: -90, Longitude: -180}},
		{name: "최댓값", latitude: "90", longitude: "180", want: &location.Coordinates{Latitude: 90, Longitude: 180}},
		{name: "위도 누락", longitude: "0", wantError: "LATITUDE and LONGITUDE"},
		{name: "경도 누락", latitude: "0", wantError: "LATITUDE and LONGITUDE"},
		{name: "위도 범위 초과", latitude: "90.01", longitude: "0", wantError: "LATITUDE"},
		{name: "경도 범위 초과", latitude: "0", longitude: "-180.01", wantError: "LONGITUDE"},
		{name: "숫자 아님", latitude: "0", longitude: "abc", wantError: "LONGITUDE"},
		{name: "NaN", latitude: "NaN", longitude: "0", wantError: "LATITUDE"},
		{name: "무한대", latitude: "0", longitude: "+Inf", wantError: "LONGITUDE"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnv()
			env["LATITUDE"] = tt.latitude
			env["LONGITUDE"] = tt.longitude

			cfg, err := config.Load(getenvFunc(env))

			if tt.wantError != "" {
				require.ErrorContains(t, err, tt.wantError)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.Location)
		})
	}
}

func TestLoad_LocationMode(t *testing.T) {
	tests := []struct {
		name       string
		env        map[string]string
		wantMode   config.LocationMode
		wantRadius float64
		wantSpeed  float64
		wantError  string
	}{
		{name: "default mode", wantMode: config.LocationFixed},
		{name: "explicit fixed", env: map[string]string{"LOCATION_MODE": "fixed"}, wantMode: config.LocationFixed},
		{name: "unknown mode", env: map[string]string{"LOCATION_MODE": "moving"}, wantError: "LOCATION_MODE"},
		{name: "wander without center", env: map[string]string{"LOCATION_MODE": "wander"}, wantError: "LATITUDE and LONGITUDE"},
		{name: "wander default", env: map[string]string{"LOCATION_MODE": "wander", "LATITUDE": "0", "LONGITUDE": "0"}, wantMode: config.LocationWander, wantRadius: 1},
		{name: "wander custom", env: map[string]string{"LOCATION_MODE": "wander", "LATITUDE": "0", "LONGITUDE": "0", "WANDER_RADIUS_KM": "2.5"}, wantMode: config.LocationWander, wantRadius: 2.5},
		{name: "non-finite radius", env: map[string]string{"LOCATION_MODE": "wander", "LATITUDE": "0", "LONGITUDE": "0", "WANDER_RADIUS_KM": "NaN"}, wantError: "WANDER_RADIUS_KM"},
		{name: "route default", env: map[string]string{"LOCATION_MODE": "route"}, wantMode: config.LocationRoute, wantSpeed: 90},
		{name: "route custom", env: map[string]string{"LOCATION_MODE": "route", "ROUTE_SPEED_KMH": "650.5"}, wantMode: config.LocationRoute, wantSpeed: 650.5},
		{name: "zero speed", env: map[string]string{"LOCATION_MODE": "route", "ROUTE_SPEED_KMH": "0"}, wantError: "ROUTE_SPEED_KMH"},
		{name: "negative speed", env: map[string]string{"LOCATION_MODE": "route", "ROUTE_SPEED_KMH": "-1"}, wantError: "ROUTE_SPEED_KMH"},
		{name: "infinite speed", env: map[string]string{"LOCATION_MODE": "route", "ROUTE_SPEED_KMH": "+Inf"}, wantError: "ROUTE_SPEED_KMH"},
		{name: "malformed speed", env: map[string]string{"LOCATION_MODE": "route", "ROUTE_SPEED_KMH": "fast"}, wantError: "ROUTE_SPEED_KMH"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnv()
			for key, value := range tt.env {
				env[key] = value
			}

			cfg, err := config.Load(getenvFunc(env))
			if tt.wantError != "" {
				require.ErrorContains(t, err, tt.wantError)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantMode, cfg.LocationMode)
			assert.Equal(t, tt.wantRadius, cfg.WanderRadiusKM)
			assert.Equal(t, tt.wantSpeed, cfg.RouteSpeedKMH)
		})
	}
}

func generateString(r rune, length int) string {
	runes := make([]rune, length)
	for i := range runes {
		runes[i] = r
	}
	return string(runes)
}
