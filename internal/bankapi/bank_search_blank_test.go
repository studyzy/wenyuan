package bankapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// A supplied q filters even when it is blank: the reference tests its query
// parameter for truthiness (engine/retain/bank_utils.py:708), and " " is true,
// so it builds a LIKE '% %' that matched nothing rather than dropping the
// filter. The earlier answer treated an all-space q as no filter at all and
// listed every bank.
func TestBlankSearchStillFilters(t *testing.T) {
	mux, store := newTestServer(t)
	for _, bank := range []string{"alpha", "beta"} {
		if _, err := store.CreateBank(t.Context(), storage.Bank{BankID: bank, Name: bank}); err != nil {
			t.Fatal(err)
		}
	}

	// An empty value is the reference's falsy case: `?q=` turns the filter off,
	// while a blank-but-nonempty one is truthy and filters everything out.
	for _, path := range []string{
		"/v1/default/banks?q=%20",
		"/v1/default/banks?q=%20%20",
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d: %s", path, rec.Code, rec.Body.String())
		}
		var response struct {
			Banks []map[string]any `json:"banks"`
			Total int              `json:"total"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		if response.Total != 0 || len(response.Banks) != 0 {
			t.Fatalf("%s listed %d banks (total %d), want none", path, len(response.Banks), response.Total)
		}
	}

	// Neither an absent q nor an empty one filters: both list every bank.
	for _, path := range []string{"/v1/default/banks", "/v1/default/banks?q="} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		var all struct {
			Total int `json:"total"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &all); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		if all.Total != 2 {
			t.Fatalf("%s listed %d banks, want both", path, all.Total)
		}
	}
}
