package reporting

import (
	"context"
	"net/http"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// A patch that reaches both the node row and the model behind it commits as one
// change. The reference runs the whole patch in one transaction, which is what
// keeps a rename from surviving the option update that failed after it; this
// pins that interleaving. Before the two writes shared a transaction, the store
// below left the node renamed with the request answered as a failure.
func TestKnowledgePatchLeavesNoRenameBehindWhenTheModelWriteFails(t *testing.T) {
	mux, store := newServerWithStore(t)
	base := "/v1/default/banks/" + bank
	page := createKnowledgePage(t, mux, base, `{"name":"Deploy","source_query":"How do we deploy?"}`)

	// A model write refuses when the deployment's embedder answers a vector the
	// deployment's dimension rejects — a misconfigured embedding model rather
	// than a hypothetical failure. The node write does not embed, so the two
	// writes stand or fall together only if they share a transaction.
	store.SetObservationEmbedder(wrongWidthEmbedder{})

	rec := send(mux, http.MethodPatch, base+"/knowledge-base/nodes/"+page.PageID,
		`{"name":"Renamed","tags":["ops","deploy"]}`)
	if rec.Code == http.StatusOK {
		t.Fatalf("a refused model write must fail the patch: %s", rec.Body.String())
	}

	node, err := store.GetKnowledgeNode(context.Background(), bank, page.PageID)
	if err != nil {
		t.Fatal(err)
	}
	if node.Title != "Deploy" {
		t.Fatalf("the rename survived the model write that failed after it: %q", node.Title)
	}
	if len(node.Tags) != 0 {
		t.Fatalf("the node's tags come from the same failed patch: %v", node.Tags)
	}
	model, err := store.GetMentalModel(context.Background(), bank, page.MentalModelID)
	if err != nil {
		t.Fatal(err)
	}
	if model.Name != "Deploy" || len(model.Tags) != 0 {
		t.Fatalf("the backing model was changed by a failed patch: name %q tags %v", model.Name, model.Tags)
	}
}

// wrongWidthEmbedder answers a vector the deployment's dimension rejects, which
// is how a mid-patch model write is made to fail.
type wrongWidthEmbedder struct{}

func (wrongWidthEmbedder) EmbedPassages(context.Context, []string) ([][]float32, error) {
	return [][]float32{{0, 1, 0}}, nil
}

func (wrongWidthEmbedder) EmbedQuery(context.Context, string) ([]float32, error) {
	return []float32{0, 1, 0}, nil
}

var _ storage.EmbeddingGenerator = wrongWidthEmbedder{}
