package location

import (
	"math"
	"time"
)

const wanderSpeedKMH = 10.0

// Wander는 기준점 주변을 일정한 속도로 도는 위치 공급자다.
type Wander struct {
	center        Coordinates
	angularRadius float64
	initialAngle  float64
	periodHours   float64
	startedAt     time.Time
}

// NewWander는 차량 ID로 radiusKM 안의 궤적과 출발 방향을 정한다.
func NewWander(center Coordinates, radiusKM float64, vehicleID string, startedAt time.Time) Wander {
	fraction := vehicleFraction(vehicleID)
	angularRadius := min(radiusKM/earthRadiusKM, math.Pi/2) * (0.5 + 0.5*fraction)
	circumferenceKM := 2 * math.Pi * earthRadiusKM * math.Sin(angularRadius)
	return Wander{
		center:        center,
		angularRadius: angularRadius,
		initialAngle:  2 * math.Pi * fraction,
		periodHours:   circumferenceKM / wanderSpeedKMH,
		startedAt:     startedAt,
	}
}

// Locate는 시작 시각부터 이동한 거리에 해당하는 원 위의 좌표를 반환한다.
func (w Wander) Locate(at time.Time) (Coordinates, bool) {
	if w.periodHours <= 0 {
		return w.center, true
	}
	elapsedHours := max(0, at.Sub(w.startedAt).Hours())
	angle := w.initialAngle + 2*math.Pi*(math.Mod(elapsedHours, w.periodHours)/w.periodHours)

	// 기준점에서 북쪽과 동쪽 방향으로 각도만큼 회전한 지점을 구한다.
	centerSin, centerCos := math.Sincos(w.center.Latitude * radiansPerDegree)
	radiusSin, radiusCos := math.Sincos(w.angularRadius)
	angleSin, angleCos := math.Sincos(angle)
	x := centerCos*radiusCos - centerSin*radiusSin*angleCos
	y := radiusSin * angleSin
	z := centerSin*radiusCos + centerCos*radiusSin*angleCos

	return Coordinates{
		Latitude:  math.Atan2(z, math.Hypot(x, y)) / radiansPerDegree,
		Longitude: wrapLongitude(w.center.Longitude + math.Atan2(y, x)/radiansPerDegree),
	}, true
}

func wrapLongitude(longitude float64) float64 {
	return math.Remainder(longitude, 360)
}
