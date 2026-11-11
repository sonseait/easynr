// Command probe reads kernel counters from inside a disposable load-test container.
package main

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
)

func main() {
	metrics := map[string]float64{}
	status, err := os.ReadFile("/proc/1/status")
	if err != nil {
		panic(err)
	}
	for _, line := range strings.Split(string(status), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && (fields[0] == "RssAnon:" || fields[0] == "RssFile:") {
			kb, _ := strconv.ParseFloat(fields[1], 64)
			metrics[fields[0]] = kb / 1024
		}
		if len(fields) >= 2 && (fields[0] == "VmHWM:" || fields[0] == "VmRSS:") {
			kb, _ := strconv.ParseFloat(fields[1], 64)
			if fields[0] == "VmHWM:" {
				metrics["processPeakRSSMiB"] = kb / 1024
			} else {
				metrics["processRSSMiB"] = kb / 1024
			}
		}
	}
	peak, err := os.ReadFile("/sys/fs/cgroup/memory.peak")
	if err != nil {
		panic(err)
	}
	n, _ := strconv.ParseFloat(strings.TrimSpace(string(peak)), 64)
	metrics["cgroupPeakMemoryMiB"] = n / (1024 * 1024)
	if info, err := os.Stat("/data/metadata.db"); err == nil {
		metrics["databaseMiB"] = float64(info.Size()) / (1024 * 1024)
	}
	cpu, err := os.ReadFile("/sys/fs/cgroup/cpu.stat")
	if err != nil {
		panic(err)
	}
	for _, line := range strings.Split(string(cpu), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "usage_usec" {
			n, _ := strconv.ParseFloat(fields[1], 64)
			metrics["cpuSeconds"] = n / 1e6
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(metrics); err != nil {
		panic(err)
	}
}
