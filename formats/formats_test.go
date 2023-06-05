package formats_test

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/hexian000/tlswrapper/formats"
)

func TestIECBytes(t *testing.T) {
	fmt.Printf("|%16s|\n", formats.IECBytes(math.NaN()))
	fmt.Printf("|%16s|\n", formats.IECBytes(-math.NaN()))
	fmt.Printf("|%16s|\n", formats.IECBytes(math.Inf(1)))
	fmt.Printf("|%16s|\n", formats.IECBytes(math.Inf(-1)))
	zero := 0.0
	fmt.Printf("|%16s|\n", formats.IECBytes(zero))
	fmt.Printf("|%16s|\n", formats.IECBytes(-zero))
	for i := 0; i < 30; i++ {
		fmt.Printf("|%16s|\n", formats.IECBytes(math.Pow10(i)))
	}
}

func TestDurationSeconds(t *testing.T) {
	d := time.Duration(1)
	for i := 0; i < 31; i++ {
		fmt.Printf("|%16s|%32s|\n", formats.DurationSeconds(d), formats.DurationNanos(d))
		d <<= 2
	}
}
