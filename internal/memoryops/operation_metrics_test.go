package memoryops

import (
	"context"
	"net/http"
	"testing"

	"github.com/studyzy/wenyuan/internal/monitoring"
	"github.com/studyzy/wenyuan/internal/storage"
)

// operationSample reads the process-wide registry's counter series for one
// operation and label set, which is what a scrape reports. The registry is a
// package global shared by every test in the process, so a case compares the
// count before and after its request rather than expecting a fixed total.
func operationSample(operation, source, budget, maxTokens, success string) float64 {
	for _, metric := range monitoring.Workload.Samples() {
		if metric.Name != monitoring.OperationTotalName {
			continue
		}
		labels := metric.Labels
		if labels["operation"] != operation || labels["source"] != source ||
			labels["budget"] != budget || labels["max_tokens"] != maxTokens ||
			labels["success"] != success {
			continue
		}
		return metric.Value
	}
	return 0
}

// operationDurationCount reads the histogram count for one operation's label
// set, which the reference records alongside the counter.
func operationDurationCount(operation, source, budget, maxTokens, success string) uint64 {
	for _, histogram := range monitoring.Workload.Histograms() {
		if histogram.Name != monitoring.OperationDurationName {
			continue
		}
		labels := histogram.Labels
		if labels["operation"] != operation || labels["source"] != source ||
			labels["budget"] != budget || labels["max_tokens"] != maxTokens ||
			labels["success"] != success {
			continue
		}
		return histogram.Count
	}
	return 0
}

// TestRecallRecordsAPIOperationMetric pins the `source="api"` operation series
// the reference emits for a served recall (api/http.py:6243). The labels are the
// contract: a client reads the default budget and token count off the series for
// a request that named neither, and its success or failure off `success`.
//
// The test fails if the recording is removed, if the source label changes, or if
// either requested label stops tracking the request.
func TestRecallRecordsAPIOperationMetric(t *testing.T) {
	mux, store := newServer(t)
	store.SeedMemory("b1", storage.RecallMatch{ID: "m1", Text: "the deploy runbook is blue-green", Type: "world"})

	// A request that names neither budget nor max_tokens reports the model's
	// defaults, which the reference's own exposition shows as mid/4096.
	before := operationSample(operationRecall, operationSourceAPI, "mid", "4096", "true")
	beforeDuration := operationDurationCount(operationRecall, operationSourceAPI, "mid", "4096", "true")
	rec := post(t, mux, "/v1/default/banks/b1/memories/recall", `{"query":"deploy runbook"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := operationSample(operationRecall, operationSourceAPI, "mid", "4096", "true"); got != before+1 {
		t.Fatalf("recall counter = %v, want %v", got, before+1)
	}
	if got := operationDurationCount(operationRecall, operationSourceAPI, "mid", "4096", "true"); got != beforeDuration+1 {
		t.Fatalf("recall duration count = %d, want %d", got, beforeDuration+1)
	}

	// A request that names both labels carries them, and a non-positive token
	// count keeps its own value rather than dropping the label.
	before = operationSample(operationRecall, operationSourceAPI, "high", "-5", "true")
	rec = post(t, mux, "/v1/default/banks/b1/memories/recall",
		`{"query":"deploy runbook","budget":"high","max_tokens":-5}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := operationSample(operationRecall, operationSourceAPI, "high", "-5", "true"); got != before+1 {
		t.Fatalf("labelled recall counter = %v, want %v", got, before+1)
	}
}

// A refused request is not an operation: the reference validates the request
// before its metric block opens, so a 422 must not move the series.
func TestRecallValidationFailureRecordsNothing(t *testing.T) {
	mux, _ := newServer(t)
	before := operationSample(operationRecall, operationSourceAPI, "mid", "4096", "false")
	beforeSuccess := operationSample(operationRecall, operationSourceAPI, "mid", "4096", "true")
	rec := post(t, mux, "/v1/default/banks/b1/memories/recall", `{"query":""}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
	if got := operationSample(operationRecall, operationSourceAPI, "mid", "4096", "false"); got != before {
		t.Fatalf("a refused request moved the failure series to %v", got)
	}
	if got := operationSample(operationRecall, operationSourceAPI, "mid", "4096", "true"); got != beforeSuccess {
		t.Fatalf("a refused request moved the success series to %v", got)
	}
}

// A client disconnect is neither a success nor a failure: the reference drops it
// from the counters entirely (metrics.py `record_operation`), so an abandoned
// request must not appear on either series.
func TestServeOperationSkipsAClientDisconnect(t *testing.T) {
	success := operationSample("disconnect_probe", operationSourceAPI, "", "", "true")
	failure := operationSample("disconnect_probe", operationSourceAPI, "", "", "false")

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := serveOperation(cancelled, "disconnect_probe", "", "", func() error {
		return context.Canceled
	}); err == nil {
		t.Fatal("a cancelled operation must report its cancellation")
	}
	if got := operationSample("disconnect_probe", operationSourceAPI, "", "", "true"); got != success {
		t.Fatalf("a disconnect counted as a success: %v", got)
	}
	if got := operationSample("disconnect_probe", operationSourceAPI, "", "", "false"); got != failure {
		t.Fatalf("a disconnect counted as a failure: %v", got)
	}

	// The same call without a cancellation is a failure and is counted, so the
	// exclusion above is about the disconnect rather than about failing calls.
	if err := serveOperation(context.Background(), "disconnect_probe", "", "", func() error {
		return context.Canceled
	}); err == nil {
		t.Fatal("the probe must still report its error")
	}
	if got := operationSample("disconnect_probe", operationSourceAPI, "", "", "false"); got != failure+1 {
		t.Fatalf("a failed operation did not count: %v", got)
	}
}

// TestReflectRecordsAPIOperationMetric pins the reflection's own series: the
// reference labels it with the resolved budget and no token count
// (api/http.py:6442), and reflects the failure outcome too.
func TestReflectRecordsAPIOperationMetric(t *testing.T) {
	mux, _ := reflectServer(t)
	before := operationSample(operationReflect, operationSourceAPI, "low", "", "true")
	if rec := post(t, mux, "/v1/default/banks/b1/reflect", `{"query":"what do we know"}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := operationSample(operationReflect, operationSourceAPI, "low", "", "true"); got != before+1 {
		t.Fatalf("reflect counter = %v, want %v", got, before+1)
	}
}

// TestRetainRecordsAPIOperationMetric pins the synchronous retain's series: the
// reference records it with no budget and no token count (api/http.py:10279).
func TestRetainRecordsAPIOperationMetric(t *testing.T) {
	mux, _ := newServer(t)
	before := operationSample(operationRetain, operationSourceAPI, "", "", "true")
	rec := post(t, mux, "/v1/default/banks/b1/memories",
		`{"items":[{"content":"the deploy runbook is blue-green"}],"async":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := operationSample(operationRetain, operationSourceAPI, "", "", "true"); got != before+1 {
		t.Fatalf("retain counter = %v, want %v", got, before+1)
	}
}

// An asynchronous retain is the worker's operation, not the API's: the request
// returns before any extraction runs, so the API series must not count it twice.
// The reference accepts the queued request without opening a metric block.
func TestAsyncRetainIsNotRecordedAsAnAPIOperation(t *testing.T) {
	mux, _ := newServer(t)
	before := operationSample(operationRetain, operationSourceAPI, "", "", "true")
	rec := post(t, mux, "/v1/default/banks/b1/memories",
		`{"items":[{"content":"the deploy runbook is blue-green"}],"async":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := operationSample(operationRetain, operationSourceAPI, "", "", "true"); got != before {
		t.Fatalf("a queued retain was counted as a completed API operation: %v", got)
	}
}
