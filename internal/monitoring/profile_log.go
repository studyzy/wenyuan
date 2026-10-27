package monitoring

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"time"

	pprofprofile "github.com/google/pprof/profile"
)

// ProfileEmitter logs a periodic CPU report, the diagnostic the reference emits
// to its log stream under WENYUAN_API_PROFILE.
//
// The reference's report is a cProfile table: per-thread CPU read from /proc,
// and the functions the process spent its time in. The per-thread table carries
// over exactly, because it is read from the same place. The function table is a
// real CPU profile: runtime/pprof samples the process for the emission window
// and github.com/google/pprof/profile — the parser the Go toolchain's own
// `go tool pprof` uses, Apache-2.0 — folds the samples into the reference's
// rows, so the numbers pprof itself would print are available in-process
// without shelling out. Each row carries tottime (the function's own sampled
// CPU), cumtime (its time including callees) and ncalls (the samples that
// landed on it), sorted by own time, the way the reference sorts.
//
// Numbers are per-window by construction. cProfile can be read live with
// getstats() while it keeps accumulating, so the reference subtracts its
// previous snapshot; Go's profiler has no non-stopping snapshot, so one window
// is sampled at a time and each report already carries the delta since the last
// one.
//
// Why the log stream rather than a file or an endpoint: the case this has to
// serve is a process dying without explanation, typically a container nobody
// can exec into. Container runtimes keep the previous container's stdout, so
// the last emission before the crash is still readable afterwards.
type ProfileEmitter struct {
	// Every is the seconds between emissions.
	Every int
	// Top is the rows per emission in the function table.
	Top int
	// Log receives the report. A nil logger uses the default one.
	Log *slog.Logger
	// SampleSeconds is how long one emission samples the CPU profile and the
	// `/proc` CPU ticks. Zero means one second.
	SampleSeconds float64
}

// Emit writes one report, sampling for a single default window.
func (p *ProfileEmitter) Emit() {
	p.emit(context.Background(), p.window())
}

// Run emits until the context is cancelled, each report covering one interval.
func (p *ProfileEmitter) Run(ctx context.Context) {
	every := p.Every
	if every < 1 {
		every = 60
	}
	window := time.Duration(every) * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		p.emit(ctx, window)
	}
}

func (p *ProfileEmitter) window() time.Duration {
	if p.SampleSeconds <= 0 {
		return time.Second
	}
	return time.Duration(p.SampleSeconds * float64(time.Second))
}

// emit samples one window and writes the report: per-thread CPU first, the
// arbiter no profiler overhead can distort, then the function table. The
// profiler spans the same window as the `/proc` sample, so the two describe one
// interval.
func (p *ProfileEmitter) emit(ctx context.Context, window time.Duration) {
	log := p.Log
	if log == nil {
		log = slog.Default()
	}
	var raw bytes.Buffer
	profiling := pprof.StartCPUProfile(&raw) == nil

	for _, row := range threadCPUSample(ctx, window) {
		log.Info(fmt.Sprintf("[profile] thread %-24s %5.2f cores", row.name, row.cores))
	}

	if profiling {
		pprof.StopCPUProfile()
		p.logFunctionTable(log, raw.Bytes(), window)
	}
	flushLogOutput()
}

// logFunctionTable parses one CPU profile and logs the reference's header and
// rows, capped at Top.
func (p *ProfileEmitter) logFunctionTable(log *slog.Logger, raw []byte, window time.Duration) {
	parsed, err := pprofprofile.Parse(bytes.NewReader(raw))
	if err != nil {
		log.Warn("[profile] report failed: " + err.Error())
		return
	}
	rows := profileRows(parsed)
	log.Info(fmt.Sprintf("[profile] %d functions active in the last %ds", len(rows), int(window.Seconds())))
	top := p.Top
	if top < 1 {
		top = 20
	}
	for _, row := range rows[:min(top, len(rows))] {
		log.Info(fmt.Sprintf("[profile] tottime=%.3f cumtime=%.3f ncalls=%d %s",
			row.tottime, row.cumtime, row.ncalls, row.label))
	}
}

// profileRow is one function's sampled cost over the window.
type profileRow struct {
	tottime float64
	cumtime float64
	ncalls  int64
	label   string
}

// profileRows folds a parsed profile into one row per function. A sample's
// stack runs leaf first, so the leaf function owns the sample's CPU and every
// function on the stack counts it toward its cumulative time — the same
// tottime/cumtime split cProfile reports. ncalls is the samples the function
// appeared in, pairing with cumtime the way cProfile's call count counts a
// caller that spent no time of its own.
func profileRows(parsed *pprofprofile.Profile) []profileRow {
	if len(parsed.SampleType) == 0 {
		return nil
	}
	cpuIndex, countIndex := profileValueIndexes(parsed)
	byLabel := map[string]*profileRow{}
	var order []string
	for _, sample := range parsed.Sample {
		if len(sample.Location) == 0 {
			continue
		}
		cpu, calls := sampleValues(sample, cpuIndex, countIndex)
		seconds := float64(cpu) / float64(time.Second)
		seen := map[string]bool{}
		for _, location := range sample.Location {
			label := locationLabel(location)
			if label == "" || seen[label] {
				continue
			}
			seen[label] = true
			row := byLabel[label]
			if row == nil {
				row = &profileRow{label: label}
				byLabel[label] = row
				order = append(order, label)
			}
			row.cumtime += seconds
			row.ncalls += calls
		}
		if leaf := locationLabel(sample.Location[0]); leaf != "" {
			if row := byLabel[leaf]; row != nil {
				row.tottime += seconds
			}
		}
	}
	rows := make([]profileRow, 0, len(order))
	for _, label := range order {
		rows = append(rows, *byLabel[label])
	}
	// The reference sorts its tuples reverse: own time first, then cumulative
	// time, then call count, with the label as the tiebreaker.
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.tottime != b.tottime {
			return a.tottime > b.tottime
		}
		if a.cumtime != b.cumtime {
			return a.cumtime > b.cumtime
		}
		if a.ncalls != b.ncalls {
			return a.ncalls > b.ncalls
		}
		return a.label > b.label
	})
	return rows
}

// profileValueIndexes finds the sample-type slots for CPU time and sample
// count. The Go CPU profile names them "cpu" and "samples"; a profile that
// names neither falls back to the conventional positions so a row is still
// produced.
func profileValueIndexes(parsed *pprofprofile.Profile) (cpu, count int) {
	cpu, count = -1, -1
	for i, sampleType := range parsed.SampleType {
		switch sampleType.Type {
		case "cpu":
			cpu = i
		case "samples":
			count = i
		}
	}
	if cpu < 0 && len(parsed.SampleType) > 1 {
		cpu = 1
	}
	if count < 0 && len(parsed.SampleType) > 0 {
		count = 0
	}
	return cpu, count
}

// sampleValues reads the CPU and count slots of one sample, tolerating a
// profile that carries fewer values than its sample types declare.
func sampleValues(sample *pprofprofile.Sample, cpuIndex, countIndex int) (cpu, calls int64) {
	if cpuIndex >= 0 && cpuIndex < len(sample.Value) {
		cpu = sample.Value[cpuIndex]
	}
	if countIndex >= 0 && countIndex < len(sample.Value) {
		calls = sample.Value[countIndex]
	}
	return cpu, calls
}

// locationLabel names one frame the way the reference names a row: the file
// base name, the line and the function, so a reader can jump straight to it.
func locationLabel(location *pprofprofile.Location) string {
	if location == nil || len(location.Line) == 0 {
		return ""
	}
	line := location.Line[0]
	if line.Function == nil {
		return ""
	}
	file := line.Function.Filename
	if idx := strings.LastIndex(file, "/"); idx >= 0 {
		file = file[idx+1:]
	}
	return fmt.Sprintf("%s:%d(%s)", file, line.Line, line.Function.Name)
}

// threadState is one thread group's CPU over a sample window.
type threadState struct {
	name  string
	cores float64
}

// threadCPUSample reads per-thread CPU from /proc, the arbiter for everything
// else in the report because no profiler's own overhead can distort it. It
// returns nothing on a platform without /proc, since a missing cross-check is
// better than a fabricated one.
func threadCPUSample(ctx context.Context, window time.Duration) []threadState {
	first := readThreadTicks()
	if len(first) == 0 {
		return nil
	}
	if !sleepContext(ctx, window) {
		return nil
	}
	second := readThreadTicks()

	totals := map[string]int64{}
	for tid, ticks := range second {
		if before, ok := first[tid]; ok && ticks.ticks > before.ticks {
			totals[ticks.name+" (tid "+tid+")"] += ticks.ticks - before.ticks
		}
	}
	// The clock tick count is per second of wall time on Linux.
	hz := float64(100)
	if value, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv("CLK_TCK")), 64); err == nil && value > 0 {
		hz = value
	}
	seconds := window.Seconds()
	if seconds <= 0 {
		seconds = 1
	}
	rows := make([]threadState, 0, len(totals))
	for name, ticks := range totals {
		rows = append(rows, threadState{name: name, cores: float64(ticks) / hz / seconds})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].cores > rows[j].cores })
	if len(rows) > 8 {
		rows = rows[:8]
	}
	return rows
}

// sleepContext waits for the window, reporting false if the context ended
// first, so a cancelled emitter stops sampling rather than blocking on it.
func sleepContext(ctx context.Context, window time.Duration) bool {
	if window <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(window)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

type threadTicks struct {
	name  string
	ticks int64
}

// readThreadTicks snapshots every thread's user+system tick count.
func readThreadTicks() map[string]threadTicks {
	entries, err := os.ReadDir("/proc/self/task")
	if err != nil {
		return nil
	}
	out := make(map[string]threadTicks, len(entries))
	for _, entry := range entries {
		tid := entry.Name()
		raw, err := os.ReadFile("/proc/self/task/" + tid + "/stat")
		if err != nil {
			continue
		}
		line := string(raw)
		close := strings.LastIndex(line, ") ")
		if close < 0 {
			continue
		}
		fields := strings.Fields(line[close+2:])
		if len(fields) < 13 {
			continue
		}
		user, errUser := strconv.ParseInt(fields[11], 10, 64)
		system, errSystem := strconv.ParseInt(fields[12], 10, 64)
		if errUser != nil || errSystem != nil {
			continue
		}
		name := strings.TrimRight(line[:close][strings.LastIndex(line[:close], "(")+1:], "0123456789")
		out[tid] = threadTicks{name: name, ticks: user + system}
	}
	return out
}

// flushLogOutput flushes the standard log handler, so the last emission
// survives the failure it exists to explain.
func flushLogOutput() {
	if syncer, ok := any(slog.Default().Handler()).(interface{ Flush() error }); ok {
		_ = syncer.Flush()
	}
}
