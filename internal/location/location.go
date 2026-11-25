// Package location은 차량 위치와 위치 공급자를 제공한다.
package location

import (
	"crypto/sha256"
	"encoding/binary"
	"math"
	"time"
)

const (
	earthRadiusKM    = 6371.0
	radiansPerDegree = math.Pi / 180
)

// vehicleFraction은 차량 ID를 고정된 [0, 1) 구간 값으로 바꾼다.
func vehicleFraction(vehicleID string) float64 {
	hash := sha256.Sum256([]byte(vehicleID))
	return float64(binary.BigEndian.Uint64(hash[:8])>>11) / (1 << 53)
}

// Coordinates는 차량의 위도와 경도다.
type Coordinates struct {
	Latitude  float64
	Longitude float64
}

// Fixed는 항상 같은 좌표를 반환하는 위치 공급자다.
type Fixed struct {
	coordinates Coordinates
}

// NewFixed는 지정한 좌표를 보고하는 고정 위치 공급자를 만든다.
func NewFixed(coordinates Coordinates) Fixed {
	return Fixed{coordinates: coordinates}
}

// Locate는 체크인 시각과 관계없이 고정 좌표를 반환한다.
func (f Fixed) Locate(_ time.Time) (Coordinates, bool) {
	return f.coordinates, true
}
