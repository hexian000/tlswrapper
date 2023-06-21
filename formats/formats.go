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
	}
	return fmt.Sprintf("%d:%02d", sign*minutes, seconds)
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
	}
	return fmt.Sprintf("%d:%02d.%03d", sign*minutes, seconds, millis)
}

func DurationNanos(d time.Duration) string {
	sign := 1
	if d < 0 {
		sign = -1
		d = -d
	}
	nanos := int64(d % 1000000000)
	d /= 1000000000
	seconds := int(d % 60)
	d /= 60
	minutes := int(d % 60)
	d /= 60
	hours := int(d % 24)
	d /= 24
	days := int(d)
	if days != 0 {
		return fmt.Sprintf("%dd%02d:%02d:%02d.%09d", sign*days, hours, minutes, seconds, nanos)
	} else if hours != 0 {
		return fmt.Sprintf("%d:%02d:%02d.%09d", sign*hours, minutes, seconds, nanos)
	}
	return fmt.Sprintf("%d:%02d.%09d", sign*minutes, seconds, nanos)
}

func Duration(d time.Duration) string {
	sign := 1
	if d < 0 {
		sign = -1
		d = -d
	}
	nanos := int64(d % 1000)
	d /= 1000
	micros := int64(d % 1000)
	d /= 1000
	millis := int64(d % 1000)
	d /= 1000
	seconds := int(d % 60)
	d /= 60
	minutes := int(d % 60)
	d /= 60
	hours := int(d % 24)
	d /= 24
	days := int(d)
	if days != 0 {
		mant := float64(seconds) + float64(millis)*1e-3 + float64(micros)*1e-6 + float64(nanos)*1e-9
		return fmt.Sprintf("%dd%02d:%02d:%02.0f", sign*days, hours, minutes, mant)
	} else if hours != 0 {
		mant := float64(seconds) + float64(millis)*1e-3 + float64(micros)*1e-6 + float64(nanos)*1e-9
		return fmt.Sprintf("%d:%02d:%02.0f", sign*hours, minutes, mant)
	} else if minutes != 0 {
		mant := float64(seconds) + float64(millis)*1e-3 + float64(micros)*1e-6 + float64(nanos)*1e-9
		return fmt.Sprintf("%d:%02.0f", sign*minutes, mant)
	} else if seconds != 0 {
		mant := float64(seconds) + float64(millis)*1e-3 + float64(micros)*1e-6 + float64(nanos)*1e-9
		if mant >= 10.0 {
			return fmt.Sprintf("%.02fs", float64(sign)*mant)
		}
		return fmt.Sprintf("%.03fs", float64(sign)*mant)
	} else if millis != 0 {
		mant := float64(millis) + float64(micros)*1e-3 + float64(nanos)*1e-6
		if mant >= 100.0 {
			return fmt.Sprintf("%.01fms", float64(sign)*mant)
		} else if mant >= 10.0 {
			return fmt.Sprintf("%.02fms", float64(sign)*mant)
		}
		return fmt.Sprintf("%.03fms", float64(sign)*mant)
	} else if micros != 0 {
		mant := float64(micros) + float64(nanos)*1e-3
		if mant >= 100.0 {
			return fmt.Sprintf("%.01fµs", float64(sign)*mant)
		} else if mant >= 10.0 {
			return fmt.Sprintf("%.02fµs", float64(sign)*mant)
		}
		return fmt.Sprintf("%.03fµs", float64(sign)*mant)
	}
	return fmt.Sprintf("%dns", int64(sign)*nanos)
}
