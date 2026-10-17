package consolidation

import (
	"context"
	"testing"

	"github.com/studyzy/wenyuan/internal/monitoring"
)

// testBatchError is a distinctive error type so the recorded error_type is
// unique to this test.
type testBatchError struct{}

func (testBatchError) Error() string { return "batch boom" }

// failureSamples returns the recorded consolidation batch-failure samples.
func failureSamples() []monitoring.Metric {
	var out []monitoring.Metric
	for _, sample := range monitoring.Workload.Samples() {
		if sample.Name == monitoring.ConsolidationBatchFailuresName {
			out = append(out, sample)
		}
	}
	return out
}

// ts-d8-consolidation-failure: a batch whose model call raised records the
// failure under the "retry" class and the error's concrete type name.
func TestRunBatchRecordsTransportFailure(t *testing.T) {
	before := len(failureSamples())

	p := &stubProvider{name: "stub", err: testBatchError{}}
	c := New(p, nil)
	_, err := c.runBatch(context.Background(), "bank", []string{"s"}, []Fact{{ID: "f1"}}, nil, DefaultSettings(), -1)
	if err == nil {
		t.Fatal("a failed model call must fail the batch")
	}

	samples := failureSamples()
	if len(samples) <= before {
		t.Fatal("consolidation batch failure not recorded")
	}
	found := false
	for _, sample := range samples {
		if sample.Labels["failure_class"] == "retry" && sample.Labels["error_type"] == "testBatchError" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no retry-class sample with error_type testBatchError: %v", samples)
	}
}

// ts-d8-consolidation-failure: the recorded family carries the retry failure
// class, the only class this service's parser can produce.
func TestRunBatchFailureClassIsRetry(t *testing.T) {
	p := &stubProvider{name: "stub", err: testBatchError{}}
	c := New(p, nil)
	if _, err := c.runBatch(context.Background(), "bank", []string{"s"}, []Fact{{ID: "f1"}}, nil, DefaultSettings(), -1); err == nil {
		t.Fatal("a failed model call must fail the batch")
	}
	for _, sample := range failureSamples() {
		if sample.Labels["failure_class"] == "retry" {
			return
		}
	}
	t.Fatalf("no retry-class sample recorded: %v", failureSamples())
}
