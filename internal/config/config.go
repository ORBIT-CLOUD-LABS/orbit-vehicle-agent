// Package config loads and validates the vehicle agent's startup configuration.
package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"time"
)

var vehicleIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// Config holds the vehicle agent's validated startup configuration.
type Config struct {
	VehicleID       string
	VehicleModel    string
	HWVersion       string
	Region          string
	EnrollmentKey   string
	OTAServerURL    string
	CheckInInterval time.Duration
	DataDir         string
	InitialVersion  string
}

// Load reads the agent configuration using getenv and validates it.
// getenv is typically os.Getenv; a fake is used in tests.
func Load(getenv func(string) string) (Config, error) {
	vehicleID, err := requireEnv(getenv, "VEHICLE_ID")
	if err != nil {
		return Config{}, err
	}
	if !vehicleIDPattern.MatchString(vehicleID) {
		return Config{}, fmt.Errorf("VEHICLE_ID %q: must contain only letters, digits, or hyphens, up to 64 characters", vehicleID)
	}

	vehicleModel, err := requireEnv(getenv, "VEHICLE_MODEL")
	if err != nil {
		return Config{}, err
	}

	hwVersion, err := requireEnv(getenv, "HW_VERSION")
	if err != nil {
		return Config{}, err
	}

	region, err := requireEnv(getenv, "REGION")
	if err != nil {
		return Config{}, err
	}

	enrollmentKey, err := requireEnv(getenv, "ENROLLMENT_KEY")
	if err != nil {
		return Config{}, err
	}

	otaServerURL, err := requireEnv(getenv, "OTA_SERVER_URL")
	if err != nil {
		return Config{}, err
	}
	if err := validateHTTPURL(otaServerURL); err != nil {
		return Config{}, fmt.Errorf("OTA_SERVER_URL %q: %w", otaServerURL, err)
	}

	checkInIntervalRaw, err := requireEnv(getenv, "CHECKIN_INTERVAL")
	if err != nil {
		return Config{}, err
	}
	checkInInterval, err := parsePositiveSeconds(checkInIntervalRaw)
	if err != nil {
		return Config{}, fmt.Errorf("CHECKIN_INTERVAL %q: %w", checkInIntervalRaw, err)
	}

	dataDir, err := requireEnv(getenv, "DATA_DIR")
	if err != nil {
		return Config{}, err
	}

	initialVersion, err := requireEnv(getenv, "INITIAL_VERSION")
	if err != nil {
		return Config{}, err
	}

	return Config{
		VehicleID:       vehicleID,
		VehicleModel:    vehicleModel,
		HWVersion:       hwVersion,
		Region:          region,
		EnrollmentKey:   enrollmentKey,
		OTAServerURL:    otaServerURL,
		CheckInInterval: checkInInterval,
		DataDir:         dataDir,
		InitialVersion:  initialVersion,
	}, nil
}

func requireEnv(getenv func(string) string, key string) (string, error) {
	value := getenv(key)
	if value == "" {
		return "", fmt.Errorf("%s: required environment variable is not set", key)
	}
	return value, nil
}

func validateHTTPURL(raw string) error {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("scheme must be http or https, got %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return fmt.Errorf("host is required")
	}
	return nil
}

func parsePositiveSeconds(raw string) (time.Duration, error) {
	seconds, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("parse integer seconds: %w", err)
	}
	if seconds <= 0 {
		return 0, fmt.Errorf("must be a positive integer, got %d", seconds)
	}
	return time.Duration(seconds) * time.Second, nil
}
