package app

import (
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// The operation types whose handler calls a model are attributed; the rest add
// no trace rows and are left alone. The names are the frontend's operation
// filter values, so a listing filtered by "reflect" finds the reflections.
func TestTraceOperationFor(t *testing.T) {
	cases := []struct {
		operationType string
		want          string
		attributed    bool
	}{
		{storage.OperationTypeRetain, "retain", true},
		{storage.OperationTypeConsolidate, "consolidation", true},
		{storage.OperationTypeRefreshMentalModel, "refresh_mental_model", true},
		{storage.OperationTypeClone, "", false},
		{storage.OperationTypeWebhookDelivery, "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := traceOperationFor(tc.operationType)
		if ok != tc.attributed || got != tc.want {
			t.Fatalf("traceOperationFor(%q) = %q, %v; want %q, %v", tc.operationType, got, ok, tc.want, tc.attributed)
		}
	}
}
