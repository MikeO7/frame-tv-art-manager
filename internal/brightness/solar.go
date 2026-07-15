// Package brightness provides solar-based brightness calculation for
// Samsung Frame TVs. It computes sun elevation using a simplified solar
// position algorithm and maps it to a brightness value using the
// Kasten-Young atmospheric attenuation model.
package brightness

import (
	"errors"
	"log/slog"
	"math"
	"time"
)

// ErrSolarDisabled is returned when solar calculations are disabled or not configured.
var ErrSolarDisabled = errors.New("solar calculation disabled")

// sunElevation calculates the sun's elevation angle in degrees for a given
// geographic position and time. Uses a simplified solar position algorithm
// based on the solar declination and hour angle.
//
// Returns negative values when the sun is below the horizon.
func sunElevation(lat, lon float64, t time.Time) float64 {
	t = t.UTC()
	hours := float64(t.Hour()) + float64(t.Minute())/60.0 + float64(t.Second())/3600.0

	jc := julianCentury(t)
	decl, eqTime := solarDeclination(jc)

	// True solar time (minutes), then the hour angle (degrees).
	tst := math.Mod(hours*60+eqTime+4*lon, 1440)
	ha := tst/4 - 180
	if tst < 0 {
		ha = tst/4 + 180
	}
	haRad := ha * math.Pi / 180
	latRad := lat * math.Pi / 180

	// Solar elevation angle.
	sinElevation := math.Sin(latRad)*math.Sin(decl) +
		math.Cos(latRad)*math.Cos(decl)*math.Cos(haRad)
	return math.Asin(sinElevation) * 180 / math.Pi
}

// julianCentury returns the Julian centuries elapsed since the J2000.0 epoch
// for time t (assumed already in UTC), the time base for the solar formulas.
func julianCentury(t time.Time) float64 {
	y := float64(t.Year())
	m := float64(t.Month())
	d := float64(t.Day())
	h := float64(t.Hour()) + float64(t.Minute())/60.0 + float64(t.Second())/3600.0

	// Adjust for Jan/Feb (treated as months 13/14 of the prior year).
	if m <= 2 {
		y--
		m += 12
	}

	a := math.Floor(y / 100)
	b := 2 - a + math.Floor(a/4)
	jd := math.Floor(365.25*(y+4716)) + math.Floor(30.6001*(m+1)) + d + h/24.0 + b - 1524.5
	return (jd - 2451545.0) / 36525.0
}

// solarDeclination returns the sun's declination (radians) and the equation of
// time (minutes) for the given Julian century, per the NOAA solar position formulas.
func solarDeclination(jc float64) (decl, eqTime float64) {
	// Mean longitude and mean anomaly of the sun (degrees).
	l0 := math.Mod(280.46646+jc*(36000.76983+jc*0.0003032), 360)
	m0 := 357.52911 + jc*(35999.05029-jc*0.0001537)
	m0Rad := m0 * math.Pi / 180
	eccentricity := 0.016708634 - jc*(0.000042037+0.0000001267*jc)

	// Equation of center and the resulting apparent longitude.
	eoc := (1.914602-jc*(0.004817+jc*0.000014))*math.Sin(m0Rad) +
		(0.019993-jc*0.000101)*math.Sin(2*m0Rad) +
		0.000289*math.Sin(3*m0Rad)
	omega := 125.04 - 1934.136*jc
	sunAppLon := l0 + eoc - 0.00569 - 0.00478*math.Sin(omega*math.Pi/180)
	sunAppLonRad := sunAppLon * math.Pi / 180

	// Corrected obliquity of the ecliptic.
	obliq := 23.0 + (26.0+(21.448-jc*(46.815+jc*(0.00059-jc*0.001813)))/60.0)/60.0
	obliqCorr := obliq + 0.00256*math.Cos(omega*math.Pi/180)
	obliqCorrRad := obliqCorr * math.Pi / 180

	decl = math.Asin(math.Sin(obliqCorrRad) * math.Sin(sunAppLonRad))

	// Equation of time (minutes).
	tanHalfObliq := math.Tan(obliqCorrRad / 2)
	y2 := tanHalfObliq * tanHalfObliq
	l0Rad := l0 * math.Pi / 180
	eqTime = 4 * (y2*math.Sin(2*l0Rad) -
		2*eccentricity*math.Sin(m0Rad) +
		4*eccentricity*y2*math.Sin(m0Rad)*math.Cos(2*l0Rad) -
		0.5*y2*y2*math.Sin(4*l0Rad) -
		1.25*eccentricity*eccentricity*math.Sin(2*m0Rad)) * 180 / math.Pi
	return decl, eqTime
}

// brightnessFromElevation maps a sun elevation angle (degrees) to a
// brightness value between minVal and maxVal using the Kasten-Young atmospheric
// attenuation model.
//
// When elevation is at or below 0° (sunset/night), returns minVal.
// At zenith (90°), returns close to maxVal.
func brightnessFromElevation(elevation float64, minVal, maxVal int) int {
	if elevation <= 0 {
		return minVal
	}

	elevRad := elevation * math.Pi / 180

	// Kasten-Young air mass formula.
	airMass := 1.0 / (math.Sin(elevRad) + 0.50572*math.Pow(elevation+6.07995, -1.6364))

	// Atmospheric attenuation: relative irradiance.
	irradiance := math.Pow(0.7, math.Pow(airMass, 0.678))

	// Map to brightness range.
	brightness := minVal + int(float64(maxVal-minVal)*irradiance)

	return brightness
}

// CalculateAt returns the solar brightness for an explicit instant.
func CalculateAt(location *SolarLocation, minVal, maxVal int, now time.Time) (*int, error) {
	if location == nil || location.Latitude == nil || location.Longitude == nil {
		return nil, ErrSolarDisabled
	}

	loc, err := time.LoadLocation(location.Timezone)
	if err != nil {
		return nil, err
	}

	now = now.In(loc)
	elevation := sunElevation(*location.Latitude, *location.Longitude, now)
	b := brightnessFromElevation(elevation, minVal, maxVal)
	return &b, nil
}

// SolarLocation represents the geographic and timezone parameters for solar calculation.
type SolarLocation struct {
	Latitude  *float64
	Longitude *float64
	Timezone  string
}

// TargetOptions contains deterministic brightness policy inputs.
type TargetOptions struct {
	Location *SolarLocation
	Min      int
	Max      int
	Manual   *int
	Now      time.Time
}

// GetTargetValueAt calculates the target brightness for an explicit instant.
func GetTargetValueAt(options TargetOptions, logger *slog.Logger) *int {
	if b := trySolarBrightnessAt(options.Location, options.Min, options.Max, logger, options.Now); b != nil {
		return b
	}

	if options.Manual != nil {
		if logger != nil {
			logger.Info("manual brightness", "value", *options.Manual)
		}
		return options.Manual
	}

	return nil
}

func trySolarBrightnessAt(
	loc *SolarLocation,
	minVal, maxVal int,
	logger *slog.Logger,
	now time.Time,
) *int {
	if loc == nil || loc.Latitude == nil || loc.Longitude == nil {
		return nil
	}

	b, err := CalculateAt(loc, minVal, maxVal, now)
	if err != nil {
		if !errors.Is(err, ErrSolarDisabled) && logger != nil {
			logger.Warn("solar brightness calculation failed", "error", err)
		}
		return nil
	}

	if logger != nil {
		logger.Info("solar brightness", "value", *b)
	}
	return b
}
