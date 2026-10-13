// 패키지 설정이 차량 에이전트의 시작 구성 로드 및 유효성을 검사합니다.
package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"time"
)

var vehicleIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

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
