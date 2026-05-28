//go:build !linux

package systemhealth

// getLoadAvg returns zero values on non-Linux platforms.
func getLoadAvg() [3]float64 {
	return [3]float64{}
}

// getSystemMemoryPercent returns 0 on non-Linux platforms.
func getSystemMemoryPercent() int {
	return 0
}
