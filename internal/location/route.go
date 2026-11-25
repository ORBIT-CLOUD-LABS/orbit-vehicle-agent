package location

import "time"

type routeSegment struct {
	from, to Coordinates
	lengthKM float64
}

// Route는 경유지를 순서대로 이동한 후 마지막 경유지에 머무르는 위치 공급자다.
type Route struct {
	segments      []routeSegment
	destination   Coordinates
	hasWaypoints  bool
	speedKMH      float64
	startOffsetKM float64
	startedAt     time.Time
}

// NewRoute는 차량마다 첫 경유지 주변에서 출발 위치가 다르도록 한다.
func NewRoute(waypoints []Coordinates, speedKMH float64, vehicleID string, startedAt time.Time) Route {
	route := Route{speedKMH: speedKMH, startedAt: startedAt}
	if len(waypoints) == 0 {
		return route
	}
	route.hasWaypoints = true
	route.destination = waypoints[len(waypoints)-1]
	for i := 1; i < len(waypoints); i++ {
		from, to := waypoints[i-1], waypoints[i]
		length := distanceKM(from, to)
		if length > 0 {
			route.segments = append(route.segments, routeSegment{from: from, to: to, lengthKM: length})
		}
	}
	if len(route.segments) > 0 {
		route.startOffsetKM = vehicleFraction(vehicleID) * min(1, route.segments[0].lengthKM/2)
	}
	return route
}

// Locate는 경과 시간에 해당하는 구간을 찾고 그 안에서 좌표를 직선 보간한다.
func (r Route) Locate(at time.Time) (Coordinates, bool) {
	distance := r.startOffsetKM + max(0, at.Sub(r.startedAt).Hours())*r.speedKMH
	for _, segment := range r.segments {
		if distance < segment.lengthKM {
			fraction := distance / segment.lengthKM
			longitudeDelta := wrapLongitude(segment.to.Longitude - segment.from.Longitude)
			return Coordinates{
				Latitude:  segment.from.Latitude + fraction*(segment.to.Latitude-segment.from.Latitude),
				Longitude: wrapLongitude(segment.from.Longitude + fraction*longitudeDelta),
			}, true
		}
		distance -= segment.lengthKM
	}
	return r.destination, r.hasWaypoints
}
