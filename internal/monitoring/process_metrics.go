package monitoring

import (
	"os"
	"runtime"
	"strconv"
	"strings"
)

// The process-metric family names. Each mirrors one of the reference's
// observable gauges, which the OpenTelemetry Prometheus exporter exposes with
// the instrument name's dots replaced by underscores.
const (
	processCPUName     = "hindsight_process_cpu_seconds"
	processMemoryName  = "hindsight_process_memory_bytes"
	processOpenFDsName = "hindsight_process_open_fds"
	processThreadsName = "hindsight_process_threads"
)

// linuxClockTicks is the kernel's USER_HZ, the unit /proc/self/stat reports CPU
// time in. It is 100 on every mainstream Linux build.
const linuxClockTicks = 100

// ProcessMetrics reports this process's CPU time, peak memory, open file
// descriptors and OS thread count — the families the reference's observable
// gauges publish. It reads them from the kernel's own accounting (/proc), the
// source the reference's getrusage/threading calls ultimately come from.
//
// It returns nil where there is no such accounting to read (a non-Linux host),
// so the families are absent rather than reported as zeros.
func ProcessMetrics() []Metric {
	if runtime.GOOS != "linux" {
		return nil
	}
	var out []Metric
	if user, system, ok := processCPUSeconds(); ok {
		out = append(out,
			Metric{Name: processCPUName, Help: "Process CPU time in seconds", Value: user, Labels: map[string]string{"type": "user"}},
			Metric{Name: processCPUName, Help: "Process CPU time in seconds", Value: system, Labels: map[string]string{"type": "system"}},
		)
	}
	if rss, ok := processPeakRSSBytes(); ok {
		out = append(out, Metric{
			Name: processMemoryName, Help: "Process memory usage in bytes",
			Value: float64(rss), Labels: map[string]string{"type": "rss_max"},
		})
	}
	if fds, ok := processOpenFDs(); ok {
		out = append(out, Metric{Name: processOpenFDsName, Help: "Number of open file descriptors", Value: float64(fds)})
	}
	if threads, ok := processThreads(); ok {
		out = append(out, Metric{Name: processThreadsName, Help: "Number of active threads", Value: float64(threads)})
	}
	return out
}

// processCPUSeconds reads the process's accumulated user and system CPU time
// from /proc/self/stat, converting clock ticks to seconds. The comm field may
// contain spaces and parentheses, so the tail is taken after its last ')'; in
// that tail utime is index 11 (field 14 overall) and stime index 12.
func processCPUSeconds() (user, system float64, ok bool) {
	raw, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0, 0, false
	}
	text := string(raw)
	end := strings.LastIndexByte(text, ')')
	if end < 0 {
		return 0, 0, false
	}
	fields := strings.Fields(text[end+1:])
	// After the comm field, index 0 is state (field 3); utime is field 14, stime
	// 15, i.e. indices 11 and 12.
	if len(fields) < 13 {
		return 0, 0, false
	}
	userTicks, err1 := strconv.ParseInt(fields[11], 10, 64)
	systemTicks, err2 := strconv.ParseInt(fields[12], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return float64(userTicks) / linuxClockTicks, float64(systemTicks) / linuxClockTicks, true
}

// processPeakRSSBytes reads the process's peak resident set size from
// /proc/self/status (VmHWM, in kB) and converts it to bytes, matching the
// reference's ru_maxrss reading.
func processPeakRSSBytes() (int64, bool) {
	value, ok := statusField("VmHWM:")
	if !ok {
		return 0, false
	}
	return value * 1024, true
}

// processThreads counts the process's OS threads from /proc/self/status,
// matching the reference's threading.active_count().
func processThreads() (int64, bool) {
	return statusField("Threads:")
}

// processOpenFDs counts the entries in /proc/self/fd, the open descriptors the
// reference counts the same way.
func processOpenFDs() (int, bool) {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0, false
	}
	return len(entries), true
}

// statusField returns the leading integer of the named /proc/self/status line
// (for example "VmHWM:" or "Threads:"), ignoring its unit suffix.
func statusField(name string) (int64, bool) {
	raw, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, name) {
			continue
		}
		rest := strings.TrimSpace(line[len(name):])
		if i := strings.IndexByte(rest, ' '); i >= 0 {
			rest = rest[:i]
		}
		value, err := strconv.ParseInt(rest, 10, 64)
		if err != nil {
			return 0, false
		}
		return value, true
	}
	return 0, false
}
