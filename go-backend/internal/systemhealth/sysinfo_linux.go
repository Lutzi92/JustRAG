//go:build linux

package systemhealth

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// getLoadAvg returns the 1-, 5-, and 15-minute CPU load averages via syscall.Sysinfo.
func getLoadAvg() [3]float64 {
	var info syscall.Sysinfo_t
	if err := syscall.Sysinfo(&info); err != nil {
		return [3]float64{}
	}

	// Sysinfo.Loads values are scaled by (1 << SI_LOAD_SHIFT) = 65536.
	const scale = 65536.0
	return [3]float64{
		float64(info.Loads[0]) / scale,
		float64(info.Loads[1]) / scale,
		float64(info.Loads[2]) / scale,
	}
}

// getSystemMemoryPercent reads /proc/meminfo and returns used-memory percentage.
func getSystemMemoryPercent() int {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()

	values := make(map[string]uint64)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		key := strings.TrimSuffix(parts[0], ":")
		val, err := strconv.ParseUint(parts[1], 10, 64)
		if err != nil {
			continue
		}
		values[key] = val
	}

	total, ok1 := values["MemTotal"]
	available, ok2 := values["MemAvailable"]
	if !ok1 || !ok2 || total == 0 {
		return 0
	}

	used := total - available
	return int(used * 100 / total)
}
