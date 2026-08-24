//go:build !linux && !darwin && !windows

package pressure

import "math"

const bytesPerGB = 1024 * 1024 * 1024

func hostLoadAvg() float64       { return 0 }
func hostMemAvailableGB() float64 { return math.MaxFloat64 } // unknown -> never block
func hostSwapTotalGB() float64    { return 0 }
func hostSwapFreeGB() float64     { return 0 }
