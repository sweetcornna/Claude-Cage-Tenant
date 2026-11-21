package cage

import (
	"os"
	"strconv"
	"strings"
)

// The machine files platformMachine reads; tests point them elsewhere.
var (
	machineMeminfo = "/proc/meminfo"
	machineVmstat  = "/proc/vmstat"
)

func platformMachine() MachineMemory {
	var reading MachineMemory
	seen := 0
	if data, err := os.ReadFile(machineMeminfo); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) != 3 || fields[2] != "kB" {
				continue
			}
			value, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil || value > 1<<40 {
				continue
			}
			switch fields[0] {
			case "MemTotal:":
				reading.Total = value << 10
				seen++
			case "MemAvailable:":
				reading.Available = value << 10
				seen++
			}
		}
	}
	reading.Measured = seen == 2 && reading.Total > 0 && reading.Available <= reading.Total
	if !reading.Measured {
		reading.Available = 0
	}
	reading.Boot = reading.Total + balloonBytes()
	return reading
}

// balloonBytes is what a virtio balloon of an outer level holds in this
// machine now: such pages leave MemTotal, but the outer level may give them
// back.
func balloonBytes() uint64 {
	data, err := os.ReadFile(machineVmstat)
	if err != nil {
		return 0
	}
	var inflated, deflated uint64
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil || value > 1<<40 {
			continue
		}
		switch fields[0] {
		case "balloon_inflate":
			inflated = value
		case "balloon_deflate":
			deflated = value
		}
	}
	if deflated >= inflated {
		return 0
	}
	return (inflated - deflated) * uint64(os.Getpagesize())
}
