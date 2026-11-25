// 패키지 설정이 차량 에이전트의 시작 구성 로드 및 유효성을 검사합니다.
package config

import (
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/location"
)

var vehicleIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// LocationMode는 체크인에서 보고할 위치를 계산하는 방식이다.
type LocationMode string

const (
	LocationFixed  LocationMode = "fixed"
	LocationWander LocationMode = "wander"
	LocationRoute  LocationMode = "route"
)

const (
	defaultWanderRadiusKM = 1.0
	defaultRouteSpeedKMH  = 90.0
)

// Config는 차량 에이전트의 검증된 시작 구성을 보유합니다.
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
	Location        *location.Coordinates
	LocationMode    LocationMode
	WanderRadiusKM  float64
	RouteSpeedKMH   float64
}

// LoadFromEnv는 환경 변수를 읽고 Config를 반환합니다.
// 필수 환경 변수가 없거나 잘못된 경우 오류를 반환합니다.

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

	coordinates, err := loadLocation(getenv)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		VehicleID:       vehicleID,
		VehicleModel:    vehicleModel,
		HWVersion:       hwVersion,
		Region:          region,
		EnrollmentKey:   enrollmentKey,
		OTAServerURL:    otaServerURL,
		CheckInInterval: checkInInterval,
		DataDir:         dataDir,
		InitialVersion:  initialVersion,
		Location:        coordinates,
	}
	if err := cfg.loadLocationMode(getenv); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c *Config) loadLocationMode(getenv func(string) string) error {
	c.LocationMode = LocationMode(getenv("LOCATION_MODE"))
	switch c.LocationMode {
	case "", LocationFixed:
		c.LocationMode = LocationFixed
	case LocationWander:
		if c.Location == nil {
			return fmt.Errorf("LOCATION_MODE wander requires LATITUDE and LONGITUDE")
		}
		radius, err := loadPositiveFloat(getenv, "WANDER_RADIUS_KM", defaultWanderRadiusKM)
		if err != nil {
			return err
		}
		c.WanderRadiusKM = radius
	case LocationRoute:
		speed, err := loadPositiveFloat(getenv, "ROUTE_SPEED_KMH", defaultRouteSpeedKMH)
		if err != nil {
			return err
		}
		c.RouteSpeedKMH = speed
	default:
		return fmt.Errorf("LOCATION_MODE %q: must be fixed, wander, or route", c.LocationMode)
	}
	return nil
}

func loadPositiveFloat(getenv func(string) string, key string, fallback float64) (float64, error) {
	raw := getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, fmt.Errorf("%s must be finite and positive", key)
	}
	return value, nil
}

func loadLocation(getenv func(string) string) (*location.Coordinates, error) {
	latitude := getenv("LATITUDE")
	longitude := getenv("LONGITUDE")
	if latitude == "" && longitude == "" {
		return nil, nil
	}
	if latitude == "" || longitude == "" {
		return nil, fmt.Errorf("LATITUDE and LONGITUDE must be set together")
	}

	lat, err := parseCoordinate("LATITUDE", latitude, 90)
	if err != nil {
		return nil, err
	}
	lon, err := parseCoordinate("LONGITUDE", longitude, 180)
	if err != nil {
		return nil, err
	}
	return &location.Coordinates{Latitude: lat, Longitude: lon}, nil
}

func parseCoordinate(key, raw string, limit float64) (float64, error) {
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	if math.IsNaN(value) || value < -limit || value > limit {
		return 0, fmt.Errorf("%s must be between %g and %g", key, -limit, limit)
	}
	return value, nil
}

func requireEnv(getenv func(string) string, key string) (string, error) {
	value := getenv(key)
	if value == "" {
		return "", fmt.Errorf("%s: 요청된 환경 변수가 설정되지 않았습니다.", key)
	}
	return value, nil
}

func validateHTTPURL(raw string) error {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("스키마는 http 또는 https여야 합니다., %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return fmt.Errorf("요청된 호스트는 필수입니다.")
	}
	return nil
}

func parsePositiveSeconds(raw string) (time.Duration, error) {
	seconds, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("정수 초를 파싱할 수 없습니다: %w", err)
	}
	if seconds <= 0 {
		return 0, fmt.Errorf("양의 정수여야 합니다., %d", seconds)
	}
	return time.Duration(seconds) * time.Second, nil
}
