//go:build windows

package pressure

const bytesPerGB = 1024 * 1024 * 1024

func hostLoadAvg() float64       { return 0 }
func hostMemAvailableGB() float64 { return 0 }
func hostSwapTotalGB() float64    { return 0 }
func hostSwapFreeGB() float64     { return 0 }
