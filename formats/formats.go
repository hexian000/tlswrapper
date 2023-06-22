package formats

import (
	"fmt"
	"math"
	"time"
)

var iecUnits = [...]string{
	"B", "KiB", "MiB", "GiB", "TiB", "PiB", "EiB", "ZiB", "YiB",
}

func isFinite(f float64) bool {
	return !math.IsNaN(f - f)
}

func IECBytes(value float64) string {
	if !isFinite(value) || value < 8192.0 {
		return fmt.Sprintf("%.0f %s", value, iecUnits[0])
	}
	n := (int(math.Log2(value)) - 3) / 10
	if n >= len(iecUnits) {
		n = len(iecUnits) - 1
	}
	v := math.Ldexp(value, n*-10)
	if v < 10.0 {
		return fmt.Sprintf("%.02f %s", v, iecUnits[n])
	}
	if v < 100.0 {
		return fmt.Sprintf("%.01f %s", v, iecUnits[n])
	}
	return fmt.Sprintf("%.0f %s", v, iecUnits[n])
}

func DurationSeconds(d time.Duration) string {
	sign := 1
	if d < 0 {
		sign = -1
		d = -d
	}
	d /= 1000000000
	second := int(d % 60)
	d /= 60
	minute := int(d % 60)
	d /= 60
	hour := int(d % 24)
	d /= 24
	day := int(d)
	if day != 0 {
		return fmt.Sprintf("%dd%02d:%02d:%02d", sign*day, hour, minute, second)
	} else if hour != 0 {
		return fmt.Sprintf("%d:%02d:%02d", sign*hour, minute, second)
	}
	return fmt.Sprintf("%d:%02d", sign*minute, second)
}

func DurationMillis(d time.Duration) string {
	sign := 1
	if d < 0 {
		sign = -1
		d = -d
	}
	d /= 1000000
	milli := int(d % 1000)
	d /= 1000
	second := int(d % 60)
	d /= 60
	minute := int(d % 60)
	d /= 60
	hour := int(d % 24)
	d /= 24
	day := int(d)
	if day != 0 {
		return fmt.Sprintf("%dd%02d:%02d:%02d.%03d", sign*day, hour, minute, second, milli)
	} else if hour != 0 {
		return fmt.Sprintf("%d:%02d:%02d.%03d", sign*hour, minute, second, milli)
	}
	return fmt.Sprintf("%d:%02d.%03d", sign*minute, second, milli)
}

func DurationNanos(d time.Duration) string {
	sign := 1
	if d < 0 {
		sign = -1
		d = -d
	}
	nano := int64(d % 1000000000)
	d /= 1000000000
	second := int(d % 60)
	d /= 60
	minute := int(d % 60)
	d /= 60
	hour := int(d % 24)
	d /= 24
	day := int(d)
	if day != 0 {
		return fmt.Sprintf("%dd%02d:%02d:%02d.%09d", sign*day, hour, minute, second, nano)
	} else if hour != 0 {
		return fmt.Sprintf("%d:%02d:%02d.%09d", sign*hour, minute, second, nano)
	}
	return fmt.Sprintf("%d:%02d.%09d", sign*minute, second, nano)
}

func Duration(d time.Duration) string {
	sign := 1
	if d < 0 {
		sign = -1
		d = -d
	}
	nano := int64(d % 1000)
	d /= 1000
	micro := int64(d % 1000)
	d /= 1000
	milli := int64(d % 1000)
	d /= 1000
	second := int(d % 60)
	d /= 60
	minute := int(d % 60)
	d /= 60
	hour := int(d % 24)
	d /= 24
	day := int(d)
	if day != 0 {
		seconds := float64(second) + float64(milli)*1e-3 + float64(micro)*1e-6 + float64(nano)*1e-9
		return fmt.Sprintf("%dd%02d:%02d:%02.0f", sign*day, hour, minute, seconds)
	} else if hour != 0 {
		seconds := float64(second) + float64(milli)*1e-3 + float64(micro)*1e-6 + float64(nano)*1e-9
		return fmt.Sprintf("%d:%02d:%02.0f", sign*hour, minute, seconds)
	} else if minute != 0 {
		seconds := float64(second) + float64(milli)*1e-3 + float64(micro)*1e-6 + float64(nano)*1e-9
		if minute >= 10 {
			return fmt.Sprintf("%d:%02.0f", sign*minute, seconds)
		}
		return fmt.Sprintf("%d:%04.01f", sign*minute, seconds)
	} else if second != 0 {
		seconds := float64(second) + float64(milli)*1e-3 + float64(micro)*1e-6 + float64(nano)*1e-9
		if seconds >= 10.0 {
			return fmt.Sprintf("%.02fs", float64(sign)*seconds)
		}
		millis := float64(second)*1e+3 + float64(milli) + float64(micro)*1e-3 + float64(nano)*1e-6
		return fmt.Sprintf("%03.0fms", float64(sign)*millis)
	} else if milli != 0 {
		millis := float64(milli) + float64(micro)*1e-3 + float64(nano)*1e-6
		if millis >= 100.0 {
			return fmt.Sprintf("%.01fms", float64(sign)*millis)
		} else if millis >= 10.0 {
			return fmt.Sprintf("%.02fms", float64(sign)*millis)
		}
		micros := float64(milli)*1e+3 + float64(micro) + float64(nano)*1e-3
		return fmt.Sprintf("%.0fµs", float64(sign)*micros)
	} else if micro != 0 {
		micros := float64(micro) + float64(nano)*1e-3
		if micros >= 100.0 {
			return fmt.Sprintf("%.01fµs", float64(sign)*micros)
		} else if micros >= 10.0 {
			return fmt.Sprintf("%.02fµs", float64(sign)*micros)
		}
		nanos := float64(micro)*1e+3 + float64(nano)
		return fmt.Sprintf("%.0fns", float64(sign)*nanos)
	}
	return fmt.Sprintf("%dns", int64(sign)*nano)
}
