package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/config"
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

func generateString(r rune, length int) string {
	runes := make([]rune, length)
	for i := range runes {
		runes[i] = r
	}
	return string(runes)
}
