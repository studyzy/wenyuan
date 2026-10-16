package monitoring

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// demoExposition is the shape our writer produces: a counter family and a
// histogram family, so merging covers both a plain sample and a multi-sample
// family.
func demoExposition(requests float64) string {
	return fmt.Sprintf(`# HELP demo_requests_total requests
# TYPE demo_requests_total counter
demo_requests_total{route="/recall"} %s
# HELP demo_seconds latency
# TYPE demo_seconds histogram
demo_seconds_bucket{le="0.1"} %s
demo_seconds_bucket{le="1"} 1
demo_seconds_bucket{le="+Inf"} 1
demo_seconds_sum 0.2
demo_seconds_count 1
`, strconv.FormatFloat(requests, 'g', -1, 64), strconv.FormatFloat(requests, 'g', -1, 64))
}

// demoBody is the byte-returning render func a worker publishes.
func demoBody(requests float64) []byte { return []byte(demoExposition(requests)) }

// testWorker builds a worker without claiming a slot or starting the publisher,
// which is what the render-path tests need.
func testWorker(dir string, slot int, render func() []byte) *WorkerMetrics {
	return &WorkerMetrics{Slot: slot, directory: dir, render: render}
}

// samples reduces an exposition to (sample name, rendered labels) -> value.
func samples(t *testing.T, text string) map[string]string {
	t.Helper()
	families, err := parseExposition(text)
	if err != nil {
		t.Fatalf("parse exposition: %v", err)
	}
	out := map[string]string{}
	for _, family := range families {
		for _, sample := range family.samples {
			out[sample.name+requestLabelStringMap(sample.labels)] = sample.value
		}
	}
	return out
}

func TestMergeExpositionsLabelsEverySampleAndKeepsWorkersApart(t *testing.T) {
	merged, err := MergeExpositions([]WorkerExposition{
		{Slot: "0", Text: demoExposition(3)},
		{Slot: "1", Text: demoExposition(5)},
	})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	got := samples(t, string(merged))

	if want := "3"; got[`demo_requests_total{api_worker="0",route="/recall"}`] != want {
		t.Fatalf("worker 0 series = %q, want %q", got[`demo_requests_total{api_worker="0",route="/recall"}`], want)
	}
	if want := "5"; got[`demo_requests_total{api_worker="1",route="/recall"}`] != want {
		t.Fatalf("worker 1 series = %q, want %q", got[`demo_requests_total{api_worker="1",route="/recall"}`], want)
	}
	// Kept apart, never summed.
	if _, ok := got[`demo_requests_total{route="/recall"}`]; ok {
		t.Fatal("series without the api_worker label present; workers were not kept apart")
	}
	// The histogram's derived samples carry the same label.
	if _, ok := got[`demo_seconds_bucket{api_worker="0",le="0.1"}`]; !ok {
		t.Fatalf("histogram bucket not labelled: %v", got)
	}
	// One family per name, not one per worker.
	if n := strings.Count(string(merged), "# TYPE demo_requests_total counter"); n != 1 {
		t.Fatalf("demo_requests_total declared %d times, want 1", n)
	}

	families, err := parseExposition(string(merged))
	if err != nil {
		t.Fatalf("parse merged: %v", err)
	}
	kinds := map[string]string{}
	for _, family := range families {
		kinds[family.name] = family.kind
	}
	if kinds["demo_requests_total"] != "counter" || kinds["demo_seconds"] != "histogram" {
		t.Fatalf("family types lost: %v", kinds)
	}
}

func TestRenderReadsOwnSeriesLiveAndPeersFromSnapshot(t *testing.T) {
	dir := t.TempDir()
	own := float64(3)
	peer := testWorker(dir, 1, func() []byte { return demoBody(9) })
	if err := peer.WriteSnapshot(); err != nil {
		t.Fatalf("peer snapshot: %v", err)
	}

	w0 := testWorker(dir, 0, func() []byte { return demoBody(own) })
	own = 7 // the snapshot, if any, is stale; render must read live

	got := samples(t, string(w0.Render()))
	if want := "7"; got[`demo_requests_total{api_worker="0",route="/recall"}`] != want {
		t.Fatalf("own series = %q, want live %q", got[`demo_requests_total{api_worker="0",route="/recall"}`], want)
	}
	if want := "9"; got[`demo_requests_total{api_worker="1",route="/recall"}`] != want {
		t.Fatalf("peer series = %q, want %q", got[`demo_requests_total{api_worker="1",route="/recall"}`], want)
	}
}

func TestRenderSkipsAStaleSnapshot(t *testing.T) {
	dir := t.TempDir()
	peer := testWorker(dir, 1, func() []byte { return demoBody(9) })
	if err := peer.WriteSnapshot(); err != nil {
		t.Fatalf("peer snapshot: %v", err)
	}
	old := time.Now().Add(-WorkerStaleAfter - 5*time.Second)
	if err := os.Chtimes(peer.snapshotPath(1), old, old); err != nil {
		t.Fatalf("age snapshot: %v", err)
	}

	got := samples(t, string(testWorker(dir, 0, func() []byte { return demoBody(1) }).Render()))
	if _, ok := got[`demo_requests_total{api_worker="1",route="/recall"}`]; ok {
		t.Fatal("stale worker 1 series present")
	}
	if _, ok := got[`demo_requests_total{api_worker="0",route="/recall"}`]; !ok {
		t.Fatalf("live worker 0 series missing: %v", got)
	}
}

func TestRenderSkipsACorruptSnapshot(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "worker-1.prom"), []byte("this is {not an exposition\n"), 0o644); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}
	got := samples(t, string(testWorker(dir, 0, func() []byte { return demoBody(2) }).Render()))
	if _, ok := got[`demo_requests_total{api_worker="1",route="/recall"}`]; ok {
		t.Fatal("corrupt worker 1 series present")
	}
	if got[`demo_requests_total{api_worker="0",route="/recall"}`] != "2" {
		t.Fatalf("live series missing after corrupt peer: %v", got)
	}
}

func TestClaimWorkerSlotDistinctAndReusable(t *testing.T) {
	dir := t.TempDir()
	first, firstLock, err := ClaimWorkerSlot(dir, 2)
	if err != nil {
		t.Fatalf("claim first: %v", err)
	}
	second, secondLock, err := ClaimWorkerSlot(dir, 2)
	if err != nil {
		t.Fatalf("claim second: %v", err)
	}
	if first != 0 || second != 1 {
		t.Fatalf("slots = %d,%d, want 0,1", first, second)
	}
	if _, _, err := ClaimWorkerSlot(dir, 2); err == nil {
		t.Fatal("claimed a third slot when both are held")
	}
	// A released slot is reused.
	if err := firstLock.Close(); err != nil {
		t.Fatalf("close first lock: %v", err)
	}
	again, againLock, err := ClaimWorkerSlot(dir, 2)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if again != 0 {
		t.Fatalf("reclaimed slot = %d, want 0", again)
	}
	_ = againLock.Close()
	_ = secondLock.Close()
}

func TestClaimWorkerSlotWritesHolderPid(t *testing.T) {
	dir := t.TempDir()
	slot, lock, err := ClaimWorkerSlot(dir, 1)
	if err != nil || slot != 0 {
		t.Fatalf("claim: slot=%d err=%v", slot, err)
	}
	defer func() { _ = lock.Close() }()
	raw, err := os.ReadFile(filepath.Join(dir, "slot-0.lock"))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	if strings.TrimSpace(string(raw)) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("lock holder = %q, want pid %d", raw, os.Getpid())
	}
}

func TestStartWorkerMetricsPublishesImmediatelyAndStops(t *testing.T) {
	dir := t.TempDir()
	worker := StartWorkerMetrics(2, func() []byte { return demoBody(1) }, dir)
	if worker == nil {
		t.Fatal("start returned nil")
	}
	if _, err := os.Stat(worker.snapshotPath(worker.Slot)); err != nil {
		t.Fatalf("snapshot not published: %v", err)
	}
	worker.Stop()
	<-worker.done
}

func TestStartWorkerMetricsDisabledOnUnusableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: an unwritable path cannot be simulated")
	}
	if worker := StartWorkerMetrics(2, func() []byte { return nil }, "/proc/definitely-not-writable/x"); worker != nil {
		t.Fatalf("expected nil worker, got slot %d", worker.Slot)
	}
}

func TestStartWorkerMetricsRendersEveryLiveWorker(t *testing.T) {
	dir := t.TempDir()
	w0 := StartWorkerMetrics(2, func() []byte { return demoBody(3) }, dir)
	if w0 == nil {
		t.Fatal("first worker nil")
	}
	defer w0.Stop()
	w1 := StartWorkerMetrics(2, func() []byte { return demoBody(5) }, dir)
	if w1 == nil {
		t.Fatal("second worker nil")
	}
	defer w1.Stop()

	// Windows use one pid; the other worker's snapshot has to be found by the
	// glob and merged, exactly as it would across processes.
	got := samples(t, string(w0.Render()))
	slots := map[string]bool{}
	for key := range got {
		if i := strings.Index(key, `api_worker="`); i >= 0 {
			rest := key[i+len(`api_worker="`):]
			slots[rest[:strings.IndexByte(rest, '"')]] = true
		}
	}
	have := make([]string, 0, len(slots))
	for slot := range slots {
		have = append(have, slot)
	}
	sort.Strings(have)
	if len(have) != 2 {
		t.Fatalf("slots in scrape = %v, want both workers", have)
	}
}

func TestMetricsHandlerWithoutWorkersHasNoApiWorkerLabel(t *testing.T) {
	h := &MetricsHandler{Extra: func() []Metric {
		return []Metric{{Name: "demo_gauge", Help: "demo", Value: 1}}
	}}
	rec := httptest.NewRecorder()
	h.Serve(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	if strings.Contains(body, WorkerLabel) {
		t.Fatalf("single-process exposition carries %s: %s", WorkerLabel, body)
	}
	if !strings.Contains(body, "demo_gauge 1") {
		t.Fatalf("own series missing: %s", body)
	}
}

func TestMetricsHandlerWithWorkersServesMergedBody(t *testing.T) {
	dir := t.TempDir()
	peer := testWorker(dir, 1, func() []byte { return demoBody(5) })
	if err := peer.WriteSnapshot(); err != nil {
		t.Fatalf("peer snapshot: %v", err)
	}
	h := &MetricsHandler{
		Extra: func() []Metric {
			return []Metric{{Name: "demo_requests_total", Help: "requests", Type: "counter", Value: 3, Labels: map[string]string{"route": "/recall"}}}
		},
	}
	worker := StartWorkerMetrics(2, h.Exposition, dir)
	if worker == nil {
		t.Fatal("worker nil")
	}
	defer worker.Stop()
	h.Workers = worker

	rec := httptest.NewRecorder()
	h.Serve(rec, httptest.NewRequest("GET", "/metrics", nil))
	got := samples(t, rec.Body.String())
	if got[`demo_requests_total{api_worker="0",route="/recall"}`] != "3" {
		t.Fatalf("own labelled series missing: %v", got)
	}
	if got[`demo_requests_total{api_worker="1",route="/recall"}`] != "5" {
		t.Fatalf("peer labelled series missing: %v", got)
	}
}
