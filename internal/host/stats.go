package host

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// HostStats contains real-time host resource usage.
type HostStats struct {
	CPUPercent float64 `json:"cpu_percent"`
	TotalRAM   uint64  `json:"total_ram"`
	UsedRAM    uint64  `json:"used_ram"`
	TotalDisk  uint64  `json:"total_disk"`
	UsedDisk   uint64  `json:"used_disk"`
}

// GetHostStats collects real-time host resource usage.
func GetHostStats() *HostStats {
	totalRAM, usedRAM := getMemoryInfo()
	totalDisk, usedDisk := getDiskInfo()
	cpuPercent := getCPUPercent()

	return &HostStats{
		CPUPercent: cpuPercent,
		TotalRAM:   totalRAM,
		UsedRAM:    usedRAM,
		TotalDisk:  totalDisk,
		UsedDisk:   usedDisk,
	}
}

// getMemoryInfo reads memory information from cgroup (if available) or falls back to /proc/meminfo.
func getMemoryInfo() (total, used uint64) {
	// Try cgroup v2 first
	if data, err := os.ReadFile("/sys/fs/cgroup/memory.max"); err == nil {
		s := strings.TrimSpace(string(data))
		if s != "max" {
			if limit, err := strconv.ParseUint(s, 10, 64); err == nil && limit > 0 {
				total = limit
				used = getCgroupMemoryUsage()
				if used > 0 {
					return total, used
				}
			}
		}
	}

	// Try cgroup v1
	if data, err := os.ReadFile("/sys/fs/cgroup/memory/memory.limit_in_bytes"); err == nil {
		s := strings.TrimSpace(string(data))
		if limit, err := strconv.ParseUint(s, 10, 64); err == nil && limit > 0 && limit < (1<<62) {
			total = limit
			if usage, err := os.ReadFile("/sys/fs/cgroup/memory/memory.usage_in_bytes"); err == nil {
				s2 := strings.TrimSpace(string(usage))
				if u, err := strconv.ParseUint(s2, 10, 64); err == nil {
					return total, u
				}
			}
		}
	}

	// Fall back to /proc/meminfo
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		var memTotal, memAvailable uint64
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			val, _ := strconv.ParseUint(fields[1], 10, 64)
			val *= 1024
			switch fields[0] {
			case "MemTotal:":
				memTotal = val
			case "MemAvailable:":
				memAvailable = val
			}
		}
		if memTotal > 0 {
			return memTotal, memTotal - memAvailable
		}
	}

	// Last resort: syscall
	var sysinfo syscall.Sysinfo_t
	syscall.Sysinfo(&sysinfo)
	totalRAM := uint64(sysinfo.Totalram) * uint64(sysinfo.Unit)
	freeRAM := uint64(sysinfo.Freeram) * uint64(sysinfo.Unit)
	return totalRAM, totalRAM - freeRAM
}

// getCgroupMemoryUsage reads current memory usage from cgroup v2.
func getCgroupMemoryUsage() uint64 {
	if data, err := os.ReadFile("/sys/fs/cgroup/memory.current"); err == nil {
		s := strings.TrimSpace(string(data))
		if val, err := strconv.ParseUint(s, 10, 64); err == nil {
			return val
		}
	}
	return 0
}

// getDiskInfo returns total and used disk space for the root filesystem.
func getDiskInfo() (total, used uint64) {
	var stat syscall.Statfs_t
	syscall.Statfs("/", &stat)

	total = stat.Blocks * uint64(stat.Bsize)
	free := stat.Bfree * uint64(stat.Bsize)
	used = total - free
	return total, used
}

// getCPUPercent reads /proc/stat twice with a 200ms interval to compute CPU usage.
func getCPUPercent() float64 {
	read := func() (idle, total uint64) {
		data, err := os.ReadFile("/proc/stat")
		if err != nil {
			return 0, 0
		}
		lines := strings.Split(string(data), "\n")
		if len(lines) == 0 {
			return 0, 0
		}
		fields := strings.Fields(lines[0])
		if len(fields) < 5 {
			return 0, 0
		}
		var sum uint64
		for i := 1; i < len(fields); i++ {
			val, _ := strconv.ParseUint(fields[i], 10, 64)
			sum += val
			if i == 4 {
				idle = val
			}
		}
		return idle, sum
	}

	idle1, total1 := read()
	time.Sleep(200 * time.Millisecond)
	idle2, total2 := read()

	totalDelta := float64(total2 - total1)
	if totalDelta == 0 {
		return 0
	}
	idleDelta := float64(idle2 - idle1)
	return (1.0 - idleDelta/totalDelta) * 100.0
}
