package cage

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// The governor runs as root in the guest; its own tests use a fake cgroup
// root, /run directory and systemctl.
func TestResourceGovernorPython(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the governor and its fakes are for Linux guests")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	output, err := exec.Command(python, "-I", "resource-governor.test.py").CombinedOutput()
	if err != nil || !strings.Contains(string(output), "OK") {
		t.Fatalf("governor tests: %v\n%s", err, output)
	}
}

// The governor's report is what the probe parses.
func TestGovernorReportFormatMatchesTheProbe(t *testing.T) {
	report := []string{"governor 1 123.5", "class control 419430400 0.50 603979776 0 0", "class sessions 537919488 0.00 872415232 0 0", "class browser 1782579200 31.00 0 4294967296 4697620480"}
	classes := parseGovernor(report, 125)
	if len(classes) != 3 || classes["browser"].High != 4294967296 || classes["control"].Protected != 603979776 || classes["browser"].Stall != 31 {
		t.Fatalf("parsed %+v", classes)
	}
}
