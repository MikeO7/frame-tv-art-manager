// Package brightness provides solar-based brightness calculation for
// Samsung Frame TVs. It computes sun elevation using a simplified solar
// position algorithm and maps it to a brightness value using the
// Kasten-Young atmospheric attenuation model.
package brightness

import (
	"math"
	"time"
)

// SunElevation calculates the sun's elevation angle in degrees for a given
// geographic position and time. Uses a simplified solar position algorithm
// based on the solar declination and hour angle.
//
// Returns negative values when the sun is below the horizon.
func SunElevation(lat, lon float64, t time.Time) float64 {
	// Convert to UTC for calculation.
	t = t.UTC()

	// Julian date calculation.
	y := float64(t.Year())
	m := float64(t.Month())
	d := float64(t.Day())
	h := float64(t.Hour()) + float64(t.Minute())/60.0 + float64(t.Second())/3600.0

	// Adjust for Jan/Feb.
	if m <= 2 {
		y--
		m += 12
	}

	jd := math.Floor(365.25*(y+4716)) + math.Floor(30.6001*(m+1)) + d + h/24.0 - 1524.5

	// Julian century from J2000.0.
	jc := (jd - 2451545.0) / 36525.0

	// Solar coordinates.
	// Mean longitude of the sun (degrees).
	l0 := math.Mod(280.46646+jc*(36000.76983+jc*0.0003032), 360)

	// Mean anomaly of the sun (degrees).
	m0 := 357.52911 + jc*(35999.05029-jc*0.0001537)
	m0Rad := m0 * math.Pi / 180

	// Equation of center.
	eoc := (1.914602-jc*(0.004817+jc*0.000014))*math.Sin(m0Rad) +
		(0.019993-jc*0.000101)*math.Sin(2*m0Rad) +
		0.000289*math.Sin(3*m0Rad)

	// Sun's true longitude.
	sunLon := l0 + eoc

	// Sun's apparent longitude.
	omega := 125.04 - 1934.136*jc
	sunAppLon := sunLon - 0.00569 - 0.00478*math.Sin(omega*math.Pi/180)
	sunAppLonRad := sunAppLon * math.Pi / 180

	// Mean obliquity of the ecliptic.
	obliq := 23.0 + (26.0+(21.448-jc*(46.815+jc*(0.00059-jc*0.001813)))/60.0)/60.0
	obliqCorr := obliq + 0.00256*math.Cos(omega*math.Pi/180)
	obliqCorrRad := obliqCorr * math.Pi / 180

	// Sun's declination.
	sinDecl := math.Sin(obliqCorrRad) * math.Sin(sunAppLonRad)
	decl := math.Asin(sinDecl)

	// Equation of time (minutes).
	tanHalfObliq := math.Tan(obliqCorrRad / 2)
	y2 := tanHalfObliq * tanHalfObliq
	l0Rad := l0 * math.Pi / 180
	eqTime := 4 * (y2*math.Sin(2*l0Rad) -
		2*math.Sin(m0Rad)*(1-2*y2*math.Cos(2*l0Rad)) +
		(0.0167*2)*math.Sin(2*m0Rad)) * 180 / math.Pi

	// True solar time (minutes).
	tst := math.Mod(h*60+eqTime+4*lon, 1440)

	// Hour angle (degrees).
	var ha float64
	if tst < 0 {
		ha = tst/4 + 180
	} else {
		ha = tst/4 - 180
	}
	haRad := ha * math.Pi / 180

	// Latitude in radians.
	latRad := lat * math.Pi / 180

	// Solar elevation angle.
	sinElevation := math.Sin(latRad)*math.Sin(decl) +
		math.Cos(latRad)*math.Cos(decl)*math.Cos(haRad)
	elevation := math.Asin(sinElevation) * 180 / math.Pi

	return elevation
}

// BrightnessFromElevation maps a sun elevation angle (degrees) to a
// brightness value between min and max using the Kasten-Young atmospheric
// attenuation model.
//
// When elevation is at or below 0° (sunset/night), returns min.
// At zenith (90°), returns close to max.
func BrightnessFromElevation(elevation float64, min, max int) int {
	if elevation <= 0 {
		return min
	}

	elevRad := elevation * math.Pi / 180

	// Kasten-Young air mass formula.
	airMass := 1.0 / (math.Sin(elevRad) + 0.50572*math.Pow(elevation+6.07995, -1.6364))

	// Atmospheric attenuation: relative irradiance.
	irradiance := math.Pow(0.7, math.Pow(airMass, 0.678))

	// Map to brightness range.
	brightness := min + int(float64(max-min)*irradiance)

	return brightness
}

// Calculate returns the brightness value for the current time and location,
// or nil if solar brightness is not applicable.
//
// Parameters lat and lon may be nil if solar is disabled.
func Calculate(lat, lon *float64, tz string, min, max int) (*int, error) {
	if lat == nil || lon == nil {
		return nil, nil
	}

	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, err
	}

	now := time.Now().In(loc)
	elevation := SunElevation(*lat, *lon, now)
	b := BrightnessFromElevation(elevation, min, max)
	return &b, nil
}
