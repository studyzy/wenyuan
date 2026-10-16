package monitoring

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// WorkerLabel is the label every series gets in a multi-worker scrape. It is
// not `worker`: the task poller already uses that key.
const WorkerLabel = "api_worker"

// WorkerSnapshotInterval is how often a worker publishes its snapshot. It is
// well under a typical scrape interval, so the other workers' series are at
// most this old.
const WorkerSnapshotInterval = 5 * time.Second

// WorkerStaleAfter is how old a snapshot must be before it belongs to a worker
// that is gone (crashed, or replaced and not yet republished under the slot).
const WorkerStaleAfter = 15 * time.Second

// ErrNoFreeWorkerSlot means every slot is already claimed. It is not a failure:
// the caller serves the plain single-process exposition instead.
var ErrNoFreeWorkerSlot = errors.New("no free worker slot")

// SharedWorkerDir is the directory this server's workers share. It is keyed by
// the supervisor (the workers' parent), so two servers on one host, or a
// restarted server, never read each other's snapshots.
func SharedWorkerDir() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("hindsight-metrics-%d", os.Getppid()))
}

// ClaimWorkerSlot claims the lowest free slot in 0..slots-1 and returns it with
// its lock file. The caller must keep the returned file open for as long as it
// publishes under the slot: closing it releases the slot. ErrNoFreeWorkerSlot
// means every slot is held.
func ClaimWorkerSlot(directory string, slots int) (int, *os.File, error) {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return 0, nil, err
	}
	if slots < 1 {
		slots = 1
	}
	for slot := range slots {
		handle, err := os.OpenFile(filepath.Join(directory, fmt.Sprintf("slot-%d.lock", slot)), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return 0, nil, err
		}
		if err := syscall.Flock(int(handle.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			_ = handle.Close()
			continue
		}
		_, _ = handle.WriteString(strconv.Itoa(os.Getpid()))
		_ = handle.Sync()
		return slot, handle, nil
	}
	return 0, nil, ErrNoFreeWorkerSlot
}

// WorkerExposition is one worker's contribution to a merged scrape: its bounded
// slot and its exposition text.
type WorkerExposition struct {
	// Slot is the worker's slot, rendered as the api_worker label value.
	Slot string
	// Text is the worker's exposition body.
	Text string
}

// expositionSample is one parsed sample line.
type expositionSample struct {
	name   string
	labels map[string]string
	value  string
}

// expositionFamily is one HELP/TYPE family with its samples, in the order they
// were parsed.
type expositionFamily struct {
	name    string
	help    string
	kind    string
	samples []expositionSample
}

// MergeExpositions merges per-worker expositions into one, labelling every
// sample with WorkerLabel set to its slot. Series are kept apart per worker,
// never summed: summing is a query-time decision, and a sum would hide exactly
// the per-worker skew this exists to show. Families seen in more than one part
// are merged under one HELP/TYPE block, in first-seen order.
func MergeExpositions(parts []WorkerExposition) ([]byte, error) {
	order := make([]string, 0, len(parts))
	families := map[string]*expositionFamily{}
	for _, part := range parts {
		parsed, err := parseExposition(part.Text)
		if err != nil {
			return nil, fmt.Errorf("worker %s: %w", part.Slot, err)
		}
		for _, family := range parsed {
			merged := families[family.name]
			if merged == nil {
				merged = &expositionFamily{name: family.name, help: family.help, kind: family.kind}
				families[family.name] = merged
				order = append(order, family.name)
			}
			for _, sample := range family.samples {
				labels := make(map[string]string, len(sample.labels)+1)
				for k, v := range sample.labels {
					labels[k] = v
				}
				labels[WorkerLabel] = part.Slot
				merged.samples = append(merged.samples, expositionSample{name: sample.name, labels: labels, value: sample.value})
			}
		}
	}

	var b strings.Builder
	for _, name := range order {
		family := families[name]
		kind := family.kind
		if kind == "" {
			kind = "untyped"
		}
		writeHelpType(&b, family.name, family.help, kind)
		for _, sample := range family.samples {
			fmt.Fprintf(&b, "%s%s %s\n", sample.name, requestLabelStringMap(sample.labels), sample.value)
		}
	}
	return []byte(b.String()), nil
}

// parseExposition parses a body our own writer produced. Every sample line must
// belong to a family already declared by a HELP or TYPE line, the value must be
// a number and the name a metric name; anything else is a corrupt snapshot and
// an error.
func parseExposition(text string) ([]expositionFamily, error) {
	var families []expositionFamily
	index := map[string]int{}
	current := -1
	ensure := func(name string) *expositionFamily {
		if i, ok := index[name]; ok {
			return &families[i]
		}
		families = append(families, expositionFamily{name: name})
		index[name] = len(families) - 1
		return &families[len(families)-1]
	}
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimRight(raw, "\r")
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "# ") {
			rest := line[2:]
			switch {
			case strings.HasPrefix(rest, "HELP "):
				name, help := splitFirstSpace(rest[len("HELP "):])
				family := ensure(name)
				family.help = help
				current = index[name]
			case strings.HasPrefix(rest, "TYPE "):
				name, kind := splitFirstSpace(rest[len("TYPE "):])
				family := ensure(name)
				family.kind = kind
				current = index[name]
			}
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		if current < 0 {
			return nil, fmt.Errorf("sample line before any family: %q", line)
		}
		sample, err := parseSample(line)
		if err != nil {
			return nil, err
		}
		families[current].samples = append(families[current].samples, sample)
	}
	return families, nil
}

// parseSample parses one `name{labels} value` line.
func parseSample(line string) (expositionSample, error) {
	sep := strings.LastIndexByte(line, ' ')
	if sep < 0 {
		return expositionSample{}, fmt.Errorf("not a sample line: %q", line)
	}
	head := strings.TrimSpace(line[:sep])
	value := strings.TrimSpace(line[sep+1:])
	if _, err := strconv.ParseFloat(value, 64); err != nil {
		return expositionSample{}, fmt.Errorf("sample %q has non-numeric value %q", head, value)
	}
	sample := expositionSample{labels: map[string]string{}, value: value}
	if open := strings.IndexByte(head, '{'); open >= 0 {
		if !strings.HasSuffix(head, "}") {
			return expositionSample{}, fmt.Errorf("unterminated label set in %q", head)
		}
		sample.name = head[:open]
		labels, err := parseLabelSet(head[open+1 : len(head)-1])
		if err != nil {
			return expositionSample{}, err
		}
		sample.labels = labels
	} else {
		sample.name = head
	}
	if !validMetricName(sample.name) {
		return expositionSample{}, fmt.Errorf("invalid metric name %q", sample.name)
	}
	return sample, nil
}

// parseLabelSet parses the inside of a `{...}` label set, undoing the escaping
// the exposition format applies to label values. A blank set is valid.
func parseLabelSet(s string) (map[string]string, error) {
	labels := map[string]string{}
	i := 0
	for i < len(s) {
		for i < len(s) && (s[i] == ' ' || s[i] == ',') {
			i++
		}
		if i >= len(s) {
			break
		}
		start := i
		for i < len(s) && s[i] != '=' {
			i++
		}
		if i >= len(s) {
			return nil, fmt.Errorf("label without '=' in %q", s)
		}
		name := strings.TrimSpace(s[start:i])
		if name == "" {
			return nil, fmt.Errorf("empty label name in %q", s)
		}
		i++ // the '='
		if i >= len(s) || s[i] != '"' {
			return nil, fmt.Errorf("unquoted label value in %q", s)
		}
		i++
		var value strings.Builder
		for i < len(s) && s[i] != '"' {
			if s[i] == '\\' && i+1 < len(s) {
				i++
				switch s[i] {
				case 'n':
					value.WriteByte('\n')
				case '\\':
					value.WriteByte('\\')
				case '"':
					value.WriteByte('"')
				default:
					value.WriteByte(s[i])
				}
				i++
				continue
			}
			value.WriteByte(s[i])
			i++
		}
		if i >= len(s) {
			return nil, fmt.Errorf("unterminated label value in %q", s)
		}
		i++ // the closing quote
		labels[name] = value.String()
	}
	return labels, nil
}

// validMetricName reports whether name matches the exposition format's metric
// name grammar.
func validMetricName(name string) bool {
	if name == "" {
		return false
	}
	for i := range len(name) {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_', c == ':':
		case c >= '0' && c <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// splitFirstSpace splits at the first space; a value with no space yields an
// empty tail.
func splitFirstSpace(s string) (string, string) {
	if i := strings.IndexByte(s, ' '); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

// WorkerMetrics is this worker's share of the multi-worker /metrics: it
// publishes its own snapshot and renders every live worker's series.
type WorkerMetrics struct {
	// Slot is the worker's bounded slot in 0..slots-1.
	Slot int

	directory string
	lockFile  *os.File
	render    func() []byte

	stop    chan struct{}
	stopOne sync.Once
	done    chan struct{}

	mu     sync.Mutex
	warned map[string]bool
	logger *slog.Logger
}

// snapshotPath is the file this worker publishes slot's exposition to.
func (w *WorkerMetrics) snapshotPath(slot int) string {
	return filepath.Join(w.directory, fmt.Sprintf("worker-%d.prom", slot))
}

// WriteSnapshot publishes this worker's exposition, atomically so a reader
// never sees half a file.
func (w *WorkerMetrics) WriteSnapshot() error {
	path := w.snapshotPath(w.Slot)
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, w.render(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Start publishes the first snapshot immediately and then keeps republishing it
// on the interval until Stop. A failed publish is logged once and never kills
// the worker.
func (w *WorkerMetrics) Start(interval time.Duration) {
	if err := w.WriteSnapshot(); err != nil {
		w.warnOnce("snapshot", fmt.Sprintf("[metrics] could not publish worker snapshot: %v", err))
	}
	go func() {
		defer close(w.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-w.stop:
				return
			case <-ticker.C:
				if err := w.WriteSnapshot(); err != nil {
					w.warnOnce("snapshot", fmt.Sprintf("[metrics] could not publish worker snapshot: %v", err))
				}
			}
		}
	}()
}

// Stop halts publishing. It is safe to call more than once.
func (w *WorkerMetrics) Stop() {
	w.stopOne.Do(func() { close(w.stop) })
}

// Render returns every live worker's series, each labelled with its slot. This
// worker's are read live; the peers' come from their latest snapshot, and a
// snapshot older than staleAfter (or corrupt) is skipped.
func (w *WorkerMetrics) Render() []byte {
	parts := []WorkerExposition{{Slot: strconv.Itoa(w.Slot), Text: string(w.render())}}
	now := time.Now()
	paths, err := filepath.Glob(filepath.Join(w.directory, "worker-*.prom"))
	if err != nil {
		return renderOrOwn(parts, w)
	}
	sort.Strings(paths)
	for _, path := range paths {
		base := filepath.Base(path)
		slot := strings.TrimSuffix(strings.TrimPrefix(base, "worker-"), ".prom")
		if slot == strconv.Itoa(w.Slot) {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			w.warnOnce("read:"+slot, fmt.Sprintf("[metrics] skipping worker %s's snapshot: %v", slot, err))
			continue
		}
		if now.Sub(info.ModTime()) > WorkerStaleAfter {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			w.warnOnce("read:"+slot, fmt.Sprintf("[metrics] skipping worker %s's snapshot: %v", slot, err))
			continue
		}
		text := string(raw)
		if _, err := parseExposition(text); err != nil {
			w.warnOnce("read:"+slot, fmt.Sprintf("[metrics] skipping worker %s's snapshot: %v", slot, err))
			continue
		}
		parts = append(parts, WorkerExposition{Slot: slot, Text: text})
	}
	return renderOrOwn(parts, w)
}

// renderOrOwn merges the parts, falling back to this worker's own exposition if
// the merge fails so a bad peer never breaks the scrape.
func renderOrOwn(parts []WorkerExposition, w *WorkerMetrics) []byte {
	merged, err := MergeExpositions(parts)
	if err != nil {
		w.warnOnce("merge", fmt.Sprintf("[metrics] could not merge worker expositions: %v", err))
		return []byte(parts[0].Text)
	}
	return merged
}

// warnOnce logs a message the first time a key is seen.
func (w *WorkerMetrics) warnOnce(key, message string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.warned == nil {
		w.warned = map[string]bool{}
	}
	if w.warned[key] {
		return
	}
	w.warned[key] = true
	logger := w.logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.Warn(message)
}

// StartWorkerMetrics claims a slot and starts publishing. It returns nil -- the
// plain single-process /metrics -- when no slot is free or the shared directory
// is unusable. It never panics: metrics must not stop the API from starting.
func StartWorkerMetrics(slots int, render func() []byte, directory string) *WorkerMetrics {
	if directory == "" {
		directory = SharedWorkerDir()
	}
	slot, lock, err := ClaimWorkerSlot(directory, slots)
	if err != nil {
		if !errors.Is(err, ErrNoFreeWorkerSlot) {
			slog.Warn(fmt.Sprintf("[metrics] worker-labelled metrics disabled: %v", err))
		} else {
			slog.Warn(fmt.Sprintf("[metrics] no free worker slot in %s; this worker serves only its own metrics", directory))
		}
		return nil
	}
	worker := &WorkerMetrics{
		Slot:      slot,
		directory: directory,
		lockFile:  lock,
		render:    render,
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
		logger:    slog.Default(),
	}
	worker.Start(WorkerSnapshotInterval)
	slog.Info(fmt.Sprintf("[metrics] worker-labelled metrics: slot %d (pid %d)", slot, os.Getpid()))
	return worker
}
