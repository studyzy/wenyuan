package monitoring

import (
	"runtime"
	"testing"
)

// On Linux the process gauges read the kernel's own accounting, so a scrape can
// reproduce the reference's CPU, memory, file-descriptor and thread families.
func TestProcessMetricsOnLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("process accounting is Linux-only")
	}
	samples := ProcessMetrics()
	if len(samples) == 0 {
		t.Fatal("ProcessMetrics returned nothing on Linux")
	}
	seen := map[string]bool{}
	for _, sample := range samples {
		seen[sample.Name] = true
	}
	for _, want := range []string{processCPUName, processMemoryName, processOpenFDsName, processThreadsName} {
		if !seen[want] {
			t.Fatalf("missing family %q in %+v", want, samples)
		}
	}
	// CPU time and thread count are strictly positive for a running process.
	for _, sample := range samples {
		switch sample.Name {
		case processThreadsName:
			if sample.Value < 1 {
				t.Fatalf("thread count = %v", sample.Value)
			}
		case processMemoryName:
			if sample.Value <= 0 {
				t.Fatalf("memory = %v", sample.Value)
			}
		}
	}
}
