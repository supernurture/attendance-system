package attendance

import (
	"math"
	"testing"

	"attendance-system/internal/api/server/modules/schedule"
)

func TestDistanceM(t *testing.T) {
	tests := map[string]struct {
		lat1, lng1, lat2, lng2 float64
		want                   float64
	}{
		"same point":             {officeLat, officeLng, officeLat, officeLng, 0},
		"200 m north":            {officeLat, officeLng, officeLat + 200/metersPerDegree, officeLng, 200},
		"one degree of meridian": {0, 0, 1, 0, metersPerDegree},
		// Along the equator a degree of longitude is as long as one of latitude.
		"one degree of equator": {0, 0, 0, 1, metersPerDegree},
		"antipodes":             {0, 0, 0, 180, math.Pi * earthRadiusM},
	}
	for name, test := range tests {
		if got := distanceM(test.lat1, test.lng1, test.lat2, test.lng2); math.Abs(got-test.want) > 0.01 {
			t.Errorf("%s: distance = %.3f m, want %.3f", name, got, test.want)
		}
	}
}

func TestLocate(t *testing.T) {
	north := func(meters float64) float64 { return officeLat + meters/metersPerDegree }
	small := schedule.OfficeLocation{ID: 1, Lat: north(150), Lng: officeLng, RadiusM: 100}  // 150 m off, too small
	large := schedule.OfficeLocation{ID: 2, Lat: north(-400), Lng: officeLng, RadiusM: 500} // 400 m off, holds it
	near := schedule.OfficeLocation{ID: 3, Lat: north(50), Lng: officeLng, RadiusM: 100}    // 50 m off, holds it

	tests := map[string]struct {
		offices []schedule.OfficeLocation
		want    int64 // 0 for none
		within  bool
	}{
		"no offices":                          {nil, 0, false},
		"outside the only one":                {[]schedule.OfficeLocation{small}, 1, false},
		"a holding office beats a nearer one": {[]schedule.OfficeLocation{small, large}, 2, true},
		"the nearer of two holding offices":   {[]schedule.OfficeLocation{large, near, small}, 3, true},
	}
	for name, test := range tests {
		officeID, within := locate(test.offices, officeLat, officeLng)
		if within != test.within || !sameID(officeID, test.want) {
			t.Errorf("%s: locate = %v, %v; want %d, %v", name, officeID, within, test.want, test.within)
		}
	}
}
