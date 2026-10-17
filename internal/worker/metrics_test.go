package worker

import (
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/monitoring"
	"github.com/studyzy/wenyuan/internal/storage"
)

// ts-d8-worker-operation: a settled operation records its duration and count
// under the worker source with the reference's operation label set.
func TestRecordOperationMetric(t *testing.T) {
	// Another test in this package runs a consumer over the same process-wide
	// registry, so the check is scoped to one operation type no other test
	// records.
	count := func() int {
		n := 0
		for _, sample := range monitoring.Workload.Samples() {
			if sample.Name == monitoring.OperationTotalName && sample.Labels["operation"] == "clone_bank" {
				n++
			}
		}
		return n
	}
	if count() != 0 {
		t.Fatal("operation family present before any operation")
	}

	var c Consumer
	c.recordOperation(storage.Operation{OperationType: storage.OperationTypeClone}, true, 1500*time.Millisecond)

	if count() != 1 {
		t.Fatal("operation total not recorded exactly once")
	}
	for _, sample := range monitoring.Workload.Samples() {
		if sample.Name != monitoring.OperationTotalName || sample.Labels["operation"] != "clone_bank" {
			continue
		}
		want := map[string]string{
			"operation": "clone_bank", "source": "worker", "budget": "", "max_tokens": "", "success": "true",
		}
		if len(sample.Labels) != len(want) {
			t.Fatalf("labels = %v, want %v", sample.Labels, want)
		}
		for name, value := range want {
			if sample.Labels[name] != value {
				t.Fatalf("label %s = %q, want %q", name, sample.Labels[name], value)
			}
		}
	}
}

// ts-d8-worker-operation-label: the retain-shaped operation types collapse to
// one label, and an unknown type is reported as such.
func TestMetricOperationLabel(t *testing.T) {
	cases := map[string]string{
		storage.OperationTypeRetain:            "retain",
		storage.AsyncRetainType:                "retain",
		storage.OperationTypeFileConvertRetain: "retain",
		storage.OperationTypeConsolidate:       "consolidation",
		"":                                     "unknown",
	}
	for operationType, want := range cases {
		if got := metricOperationLabel(operationType); got != want {
			t.Fatalf("metricOperationLabel(%q) = %q, want %q", operationType, got, want)
		}
	}
}
