package formats

import (
	"fmt"
	"math"
	"time"
)

var iec_units = [...]string{
	"B", "KiB", "MiB", "GiB", "TiB", "PiB", "EiB", "ZiB", "YiB",
}

func isFinite(f float64) bool {
	return !math.IsNaN(f - f)
}

func IECBytes(value float64) string {
	if !isFinite(value) || value < 8192.0 {
		return fmt.Sprintf("%.0f %s", value, iec_units[0])
	}
	n := (int(math.Log2(value)) - 3) / 10
	if n >= len(iec_units) {
		n = len(iec_units) - 1
	}
	v := math.Ldexp(value, n*-10)
	if v < 10.0 {
		return fmt.Sprintf("%.02f %s", v, iec_units[n])
	}
	if v < 100.0 {
		return fmt.Sprintf("%.01f %s", v, iec_units[n])
	}
	return fmt.Sprintf("%.0f %s", v, iec_units[n])
}

func DurationSeconds(d time.Duration) string {
	sign := 1
	if d < 0 {
		sign = -1
		d = -d
	}
	d /= 1000000000
	seconds := int(d % 60)
	d /= 60
	minutes := int(d % 60)
	d /= 60
	hours := int(d % 24)
	d /= 24
	days := int(d)
	if days != 0 {
		return fmt.Sprintf("%dd%02d:%02d:%02d", sign*days, hours, minutes, seconds)
	} else if hours != 0 {
		return fmt.Sprintf("%d:%02d:%02d", sign*hours, minutes, seconds)
	} else if minutes != 0 {
		return fmt.Sprintf("%d:%02d", sign*minutes, seconds)
	}
	return fmt.Sprintf("%d", sign*seconds)
}

func DurationMillis(d time.Duration) string {
	sign := 1
	if d < 0 {
		sign = -1
		d = -d
	}
	d /= 1000000
	millis := int(d % 1000)
	d /= 1000
	seconds := int(d % 60)
	d /= 60
	minutes := int(d % 60)
	d /= 60
	hours := int(d % 24)
	d /= 24
	days := int(d)
	if days != 0 {
		return fmt.Sprintf("%dd%02d:%02d:%02d.%03d", sign*days, hours, minutes, seconds, millis)
	} else if hours != 0 {
		return fmt.Sprintf("%d:%02d:%02d.%03d", sign*hours, minutes, seconds, millis)
	} else if minutes != 0 {
		return fmt.Sprintf("%d:%02d.%03d", sign*minutes, seconds, millis)
	}
	return fmt.Sprintf("%d.%03d", sign*seconds, millis)
}

func DurationNanos(d time.Duration) string {
	sign := 1
	if d < 0 {
		sign = -1
		d = -d
	}
	nanos := int(d % 1000)
	d /= 1000
	micros := int(d % 1000)
	d /= 1000
	millis := int(d % 1000)
	d /= 1000
	seconds := int(d % 60)
	d /= 60
	minutes := int(d % 60)
	d /= 60
	hours := int(d % 24)
	d /= 24
	days := int(d)
	if days != 0 {
		return fmt.Sprintf("%dd%02d:%02d:%02d.%03d%03d%03d", sign*days, hours, minutes, seconds, millis, micros, nanos)
	} else if hours != 0 {
		return fmt.Sprintf("%d:%02d:%02d.%03d%03d%03d", sign*hours, minutes, seconds, millis, micros, nanos)
	} else if minutes != 0 {
		return fmt.Sprintf("%d:%02d.%03d%03d%03d", sign*minutes, seconds, millis, micros, nanos)
	}
	return fmt.Sprintf("%d.%03d%03d%03d", sign*seconds, millis, micros, nanos)
}
