package cage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLinuxMachineReadingCountsTheOuterBalloon(t *testing.T) {
	dir := t.TempDir()
	oldMeminfo, oldVmstat := machineMeminfo, machineVmstat
	t.Cleanup(func() { machineMeminfo, machineVmstat = oldMeminfo, oldVmstat })
	machineMeminfo, machineVmstat = filepath.Join(dir, "meminfo"), filepath.Join(dir, "vmstat")
	if err := os.WriteFile(machineMeminfo, []byte("MemTotal:        4194304 kB\nMemFree: 100 kB\nMemAvailable:    1048576 kB\n"), 0600); err != nil {
		t.Fatal(err)
	}
	pages := uint64(4<<30) / uint64(os.Getpagesize())
	if err := os.WriteFile(machineVmstat, []byte("nr_free_pages 1\nballoon_inflate "+itoa(pages+10)+"\nballoon_deflate 10\n"), 0600); err != nil {
		t.Fatal(err)
	}
	reading := platformMachine()
	if !reading.Measured || reading.Total != 4<<30 || reading.Available != 1<<30 || reading.Boot != 8<<30 {
		t.Fatalf("ballooned machine %+v", reading)
	}
	// Nothing available is a reading, not an unknown.
	if err := os.WriteFile(machineMeminfo, []byte("MemTotal: 4194304 kB\nMemAvailable: 0 kB\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if reading := platformMachine(); !reading.Measured || reading.Available != 0 {
		t.Fatalf("empty machine %+v", reading)
	}
	if err := os.WriteFile(machineMeminfo, []byte("MemTotal: 4194304 kB\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if reading := platformMachine(); reading.Measured {
		t.Fatalf("incomplete reading taken as measured %+v", reading)
	}
}

func itoa(n uint64) string {
	digits := []byte{}
	for {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
		if n == 0 {
			return string(digits)
		}
	}
}
