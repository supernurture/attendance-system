package attendance

import (
	"math"

	"attendance-system/internal/api/server/modules/schedule"
)

const earthRadiusM = 6_371_008.8 // the mean radius

// distanceM is the haversine distance between two points, in meters.
func distanceM(lat1, lng1, lat2, lng2 float64) float64 {
	const rad = math.Pi / 180
	sinLat, sinLng := math.Sin((lat2-lat1)*rad/2), math.Sin((lng2-lng1)*rad/2)
	a := sinLat*sinLat + math.Cos(lat1*rad)*math.Cos(lat2*rad)*sinLng*sinLng
	return 2 * earthRadiusM * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// locate picks the nearest office whose circle holds the point, else the nearest office at all; nil with none.
func locate(offices []schedule.OfficeLocation, lat, lng float64) (officeID *int64, within bool) {
	nearest := math.Inf(1)
	for _, office := range offices {
		distance := distanceM(lat, lng, office.Lat, office.Lng)
		inside := distance <= float64(office.RadiusM)
		// An office holding the point beats any that does not, however close that one is.
		if (inside && !within) || (inside == within && distance < nearest) {
			officeID, within, nearest = &office.ID, inside, distance
		}
	}
	return officeID, within
}
