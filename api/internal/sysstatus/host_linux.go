//go:build linux

package sysstatus

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// readHost reads the machine's vital signs from /proc and the filesystems.
func readHost(dataDir string) Host {
	host := Host{Supported: true, CPUs: runtime.NumCPU()}

	if raw, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(raw))
		for i := 0; i < len(host.Load) && i < len(fields); i++ {
			host.Load[i], _ = strconv.ParseFloat(fields[i], 64)
		}
	}
	if raw, err := os.ReadFile("/proc/uptime"); err == nil {
		if fields := strings.Fields(string(raw)); len(fields) > 0 {
			if seconds, err := strconv.ParseFloat(fields[0], 64); err == nil {
				host.Uptime = time.Duration(seconds) * time.Second
			}
		}
	}
	if file, err := os.Open("/proc/meminfo"); err == nil {
		defer file.Close()
		values := map[string]*uint64{"MemTotal:": &host.MemoryTotal, "MemAvailable:": &host.MemoryFree, "SwapTotal:": &host.SwapTotal, "SwapFree:": &host.SwapFree}
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) < 2 {
				continue
			}
			if target, ok := values[fields[0]]; ok {
				kib, _ := strconv.ParseUint(fields[1], 10, 64)
				*target = kib * 1024
			}
		}
	}

	// The root filesystem, and the data root when it lives on a disk of its own.
	seen := map[uint64]bool{}
	for _, path := range []string{"/", dataDir} {
		if path == "" {
			continue
		}
		var info syscall.Stat_t
		if err := syscall.Stat(path, &info); err != nil || seen[uint64(info.Dev)] { //nolint:unconvert // Dev is not uint64 on every architecture
			continue
		}
		seen[uint64(info.Dev)] = true //nolint:unconvert // as above
		var fs syscall.Statfs_t
		if err := syscall.Statfs(path, &fs); err != nil || fs.Blocks == 0 {
			continue
		}
		blockSize := uint64(fs.Bsize) //nolint:gosec // a block size is positive
		disk := Disk{Path: path, TotalBytes: fs.Blocks * blockSize, FreeBytes: fs.Bavail * blockSize}
		disk.UsedPercent = 100 - float64(disk.FreeBytes)*100/float64(disk.TotalBytes)
		host.Disks = append(host.Disks, disk)
	}
	return host
}
