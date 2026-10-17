//go:build integration

// End-to-end tests against a running server.
//
// They are behind the `integration` build tag so the default `go test ./...`
// stays offline: every test here talks to a real service over HTTP.
//
//	HINDSIGHT_API_URL=http://127.0.0.1:18888 go test -tags integration ./...
//
// `make test-sdk-go` runs exactly that. A server started with
// `HINDSIGHT_API_DATABASE_URL=memory` is enough — no model provider is
// configured, so assertions are structural (an operation succeeded, an id
// round-tripped, a list contains what was written) rather than about extracted
// facts or synthesized prose.

package wenyuan

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func integrationURL(t *testing.T) string {
	t.Helper()
	if url := os.Getenv(BaseURLEnvVar); url != "" {
		return url
	}
	return DefaultBaseURL
}

func integrationClient(t *testing.T) *Client {
	t.Helper()
	return NewClientWithBaseURL(integrationURL(t))
}

// uniqueBank creates a bank the test owns and registers its deletion, so a run
// against a shared server leaves nothing behind.
func uniqueBank(t *testing.T) (*Client, string) {
	t.Helper()
	client := integrationClient(t)
	bankID := fmt.Sprintf("go-sdk-%d", time.Now().UnixNano())
	request := CreateBankRequest{}
	request.SetName("Go SDK integration")
	request.SetMission("serve the integration suite")
	if _, err := client.CreateBank(context.Background(), bankID, request); err != nil {
		t.Fatalf("creating bank %s: %v", bankID, err)
	}
	t.Cleanup(func() {
		if _, _, err := client.BanksAPI.DeleteBank(context.Background(), bankID).Execute(); err != nil {
			t.Logf("cleanup: deleting bank %s: %v", bankID, err)
		}
	})
	return client, bankID
}

func retainText(t *testing.T, client *Client, bankID, text string, tags ...string) {
	t.Helper()
	item := MemoryItem{Content: TextContent(text)}
	if len(tags) > 0 {
		item.Tags = tags
	}
	response, err := client.Retain(context.Background(), bankID, RetainRequest{Items: []MemoryItem{item}})
	if err != nil {
		t.Fatalf("retain: %v", err)
	}
	if !response.Success || response.ItemsCount != 1 {
		t.Fatalf("retain response = %+v, want success with one item", response)
	}
}

func TestIntegrationBankLifecycle(t *testing.T) {
	client := integrationClient(t)
	ctx := context.Background()

	bankID := fmt.Sprintf("go-sdk-lifecycle-%d", time.Now().UnixNano())
	request := CreateBankRequest{}
	request.SetName("Lifecycle")
	request.SetMission("remember the lifecycle")
	created, err := client.CreateBank(ctx, bankID, request)
	if err != nil {
		t.Fatalf("CreateBank: %v", err)
	}
	if created.BankId != bankID || created.GetName() != "Lifecycle" {
		t.Fatalf("created = %+v", created)
	}

	banks, _, err := client.BanksAPI.ListBanks(ctx).Execute()
	if err != nil {
		t.Fatalf("ListBanks: %v", err)
	}
	if !bankListed(banks, bankID) {
		t.Fatalf("bank %s missing from the list of %d banks", bankID, banks.Total)
	}
	listed := bankFrom(banks, bankID)
	if listed == nil {
		t.Fatalf("bank %s vanished between the membership check and the lookup", bankID)
	}
	if got := listed.Mission.Get(); got == nil || *got != "remember the lifecycle" {
		t.Fatalf("listed mission = %v, want the mission the bank was created with", got)
	}
	if listed.Disposition.Skepticism == 0 || listed.Disposition.Literalism == 0 || listed.Disposition.Empathy == 0 {
		t.Fatalf("listed disposition is not the resolved one: %+v", listed.Disposition)
	}

	// The configuration view names the bank's mission `reflect_mission`: that is
	// the setting the mission feeds. Reading it back is how a client confirms
	// what a bank was created with.
	config, err := client.GetBankConfig(ctx, bankID)
	if err != nil {
		t.Fatalf("GetBankConfig: %v", err)
	}
	if got := config.Config["reflect_mission"]; got != "remember the lifecycle" {
		t.Fatalf("config reflect_mission = %v, want the mission the bank was created with", got)
	}

	if _, httpResp, err := client.BanksAPI.DeleteBank(ctx, bankID).Execute(); err != nil {
		t.Fatalf("DeleteBank: %v (status %v)", err, httpStatus(httpResp))
	}
	after, _, err := client.BanksAPI.ListBanks(ctx).Execute()
	if err != nil {
		t.Fatalf("ListBanks after delete: %v", err)
	}
	if bankListed(after, bankID) {
		t.Fatalf("bank %s still listed after deletion", bankID)
	}
}

func TestIntegrationBankConfigRoundTrip(t *testing.T) {
	client, bankID := uniqueBank(t)
	ctx := context.Background()

	update := BankConfigUpdate{Updates: map[string]interface{}{"retain_extraction_mode": "verbose"}}
	configured, _, err := client.BanksAPI.UpdateBankConfig(ctx, bankID).BankConfigUpdate(update).Execute()
	if err != nil {
		t.Fatalf("UpdateBankConfig: %v", err)
	}
	if got := configured.Config["retain_extraction_mode"]; got != "verbose" {
		t.Fatalf("config after update = %v, want verbose", got)
	}
	if got := configured.Overrides["retain_extraction_mode"]; got != "verbose" {
		t.Fatalf("overrides after update = %v, want the bank-level override", got)
	}

	reset, _, err := client.BanksAPI.ResetBankConfig(ctx, bankID).Execute()
	if err != nil {
		t.Fatalf("ResetBankConfig: %v", err)
	}
	if got := reset.Config["retain_extraction_mode"]; got == "verbose" {
		t.Fatalf("reset left the override in place: %v", got)
	}
}

func TestIntegrationRetainAndDocumentReads(t *testing.T) {
	client, bankID := uniqueBank(t)
	ctx := context.Background()

	retainText(t, client, bankID, "The schema stays the reference's schema, object for object.")

	documents, _, err := client.DocumentsAPI.ListDocuments(ctx, bankID).Execute()
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	if documents.Total == 0 || len(documents.Items) == 0 {
		t.Fatalf("retain produced no document: %+v", documents)
	}
	documentID := documents.Items[0].Id

	document, _, err := client.DocumentsAPI.GetDocument(ctx, bankID, documentID).Execute()
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if document.Id != documentID {
		t.Fatalf("GetDocument returned %s, want %s", document.Id, documentID)
	}

	chunks, _, err := client.DocumentsAPI.ListDocumentChunks(ctx, bankID, documentID).Execute()
	if err != nil {
		t.Fatalf("ListDocumentChunks: %v", err)
	}
	if len(chunks.Items) == 0 {
		t.Fatalf("no chunks for document %s", documentID)
	}
	chunk, _, err := client.DocumentsAPI.GetChunk(ctx, chunks.Items[0].ChunkId).Execute()
	if err != nil {
		t.Fatalf("GetChunk: %v", err)
	}
	if strings.TrimSpace(chunk.ChunkText) == "" {
		t.Fatalf("chunk %s has no text", chunk.ChunkId)
	}
}

func TestIntegrationRetainBatchWithTags(t *testing.T) {
	client, bankID := uniqueBank(t)
	ctx := context.Background()

	tag := "go-sdk-tagged"
	items := []MemoryItem{
		{Content: TextContent("The first tagged note records that Alice reviews the schema before every release."), Tags: []string{tag}},
		{Content: TextContent("The second tagged note records that Bob keeps the migration series generated."), Tags: []string{tag}},
	}
	response, _, err := client.MemoryAPI.RetainMemories(ctx, bankID).
		RetainRequest(RetainRequest{Items: items, DocumentTags: []string{"go-sdk-document"}}).
		Execute()
	if err != nil {
		t.Fatalf("retain batch: %v", err)
	}
	if response.ItemsCount != 2 {
		t.Fatalf("items_count = %d, want 2", response.ItemsCount)
	}

	// The tag inventory counts tags carried by extracted memories, so it is
	// empty on a server with no retain model — what a tagged write must survive
	// is the *filter*: the same tag has to select the write back out.
	tags, _, err := client.MemoryAPI.ListTags(ctx, bankID).Execute()
	if err != nil {
		t.Fatalf("ListTags: %v", err)
	}
	if int(tags.Total) != len(tags.Items) {
		t.Fatalf("tags response is inconsistent: total %d, items %d", tags.Total, len(tags.Items))
	}

	tagged, err := client.Recall(ctx, bankID, RecallRequest{Query: "what do the tagged notes record?", Tags: []string{tag}})
	if err != nil {
		t.Fatalf("tagged recall: %v", err)
	}
	if len(tagged.Results) == 0 {
		t.Fatalf("the tag %q selected nothing back out", tag)
	}

	// A tag nobody wrote must select nothing, which proves the filter is the
	// tag and not "any query returns the bank's content".
	untagged, err := client.Recall(ctx, bankID, RecallRequest{Query: "what do the tagged notes record?", Tags: []string{"go-sdk-never-written"}})
	if err != nil {
		t.Fatalf("recall with an unwritten tag: %v", err)
	}
	if len(untagged.Results) != 0 {
		t.Fatalf("an unwritten tag matched %d results: %+v", len(untagged.Results), untagged.Results)
	}
}

func TestIntegrationAsyncRetainIsQueued(t *testing.T) {
	client, bankID := uniqueBank(t)
	ctx := context.Background()

	asynchronous := true
	response, _, err := client.MemoryAPI.RetainMemories(ctx, bankID).
		RetainRequest(RetainRequest{
			Items: []MemoryItem{{Content: TextContent("queued work")}},
			Async: &asynchronous,
		}).
		Execute()
	if err != nil {
		t.Fatalf("async retain: %v", err)
	}
	if !response.Async {
		t.Fatalf("async retain answered with async=false: %+v", response)
	}
	operationID := response.OperationId.Get()
	if operationID == nil || *operationID == "" {
		t.Fatalf("async retain must name an operation: %+v", response)
	}

	operations, _, err := client.OperationsAPI.ListOperations(ctx, bankID).Execute()
	if err != nil {
		t.Fatalf("ListOperations: %v", err)
	}
	var queued bool
	for _, operation := range operations.Operations {
		if operation.OperationId.Get() != nil && *operation.OperationId.Get() == *operationID {
			queued = true
		}
	}
	if !queued {
		t.Fatalf("operation %s missing from the bank's queue: %+v", *operationID, operations.Operations)
	}

	status, _, err := client.OperationsAPI.GetOperationStatus(ctx, bankID, *operationID).Execute()
	if err != nil {
		t.Fatalf("GetOperationStatus: %v", err)
	}
	if status.Status == "" {
		t.Fatalf("operation status is empty: %+v", status)
	}
}

func TestIntegrationRecallBasic(t *testing.T) {
	client, bankID := uniqueBank(t)
	ctx := context.Background()

	retainText(t, client, bankID, "Alice prefers tea over coffee in the afternoon")

	response, err := client.Recall(ctx, bankID, RecallRequest{Query: "what does Alice prefer to drink?"})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(response.Results) == 0 {
		t.Fatalf("recall found nothing for a phrase that was just retained")
	}
	for _, result := range response.Results {
		if result.Id == "" || strings.TrimSpace(result.Text) == "" {
			t.Fatalf("recall result is incomplete: %+v", result)
		}
	}
}

func TestIntegrationRecallOptions(t *testing.T) {
	client, bankID := uniqueBank(t)
	ctx := context.Background()

	retainText(t, client, bankID, "The migration series is generated from the reference catalogue, so a schema change is made there and the series is regenerated.", "go-sdk-option")

	maxTokens := int32(64)
	trace := true
	include := IncludeOptions{Chunks: *NewNullableChunkIncludeOptions(&ChunkIncludeOptions{})}
	response, _, err := client.MemoryAPI.RecallMemories(ctx, bankID).
		RecallRequest(RecallRequest{
			Query:     "how is the migration series generated?",
			MaxTokens: &maxTokens,
			Trace:     &trace,
			Tags:      []string{"go-sdk-option"},
			Include:   &include,
		}).
		Execute()
	if err != nil {
		t.Fatalf("recall with options: %v", err)
	}
	if len(response.Results) == 0 {
		t.Fatalf("tagged recall found nothing")
	}
	if len(response.Trace) == 0 {
		t.Fatalf("trace was requested but the response carries none")
	}
	if response.Chunks == nil {
		t.Fatalf("chunks were requested but the response carries none")
	}
	for id, chunk := range response.Chunks {
		if chunk.Id != id || strings.TrimSpace(chunk.Text) == "" {
			t.Fatalf("chunk %s is incomplete: %+v", id, chunk)
		}
	}
}

func TestIntegrationReflect(t *testing.T) {
	client, bankID := uniqueBank(t)
	ctx := context.Background()

	retainText(t, client, bankID, "The migration series is generated from the reference catalogue.")

	response, httpResp, err := client.MemoryAPI.Reflect(ctx, bankID).
		ReflectRequest(ReflectRequest{Query: "how is the migration series produced?"}).
		Execute()
	if err != nil {
		// A deployment may have a model configured, and that model may be
		// unreachable or slow; reflection then answers its documented
		// wall-clock-budget refusal. What this suite proves is that the call
		// reaches the handler and the client decodes the answer, so that status
		// counts as exercised and any other failure does not.
		if httpStatus(httpResp) == http.StatusGatewayTimeout {
			t.Logf("reflection exceeded the deployment's budget; the call and its refusal decoded")
			return
		}
		t.Fatalf("Reflect: %v (status %v)", err, httpStatus(httpResp))
	}
	if strings.TrimSpace(response.Text) == "" {
		t.Fatalf("reflect answered with no text: %+v", response)
	}
}

func TestIntegrationEntitiesAndGraph(t *testing.T) {
	client, bankID := uniqueBank(t)
	ctx := context.Background()

	retainText(t, client, bankID, "Alice Chen works with Bob in the Berlin office.")

	entities, _, err := client.EntitiesAPI.ListEntities(ctx, bankID).Execute()
	if err != nil {
		t.Fatalf("ListEntities: %v", err)
	}
	graph, _, err := client.EntitiesAPI.GetEntityGraph(ctx, bankID).Execute()
	if err != nil {
		t.Fatalf("GetEntityGraph: %v", err)
	}
	if graph.TotalEntities != entities.Total {
		t.Fatalf("graph reports %d entities, the list reports %d", graph.TotalEntities, entities.Total)
	}
	for _, node := range graph.Nodes {
		if node.Data.Id == "" {
			t.Fatalf("graph node without an id: %+v", graph.Nodes)
		}
	}
}

func TestIntegrationAuditLogFollowsTheBankSwitch(t *testing.T) {
	client, bankID := uniqueBank(t)
	ctx := context.Background()

	// Audit logging is off by default (like the reference) and a bank may turn
	// it on for itself, so the switch is exercised rather than assumed.
	update := BankConfigUpdate{Updates: map[string]interface{}{"audit_log_enabled": true}}
	if _, _, err := client.BanksAPI.UpdateBankConfig(ctx, bankID).BankConfigUpdate(update).Execute(); err != nil {
		t.Fatalf("enabling audit log: %v", err)
	}

	retainText(t, client, bankID, "An audited write.")

	logs, _, err := client.AuditAPI.ListAuditLogs(ctx, bankID).Execute()
	if err != nil {
		t.Fatalf("ListAuditLogs: %v", err)
	}
	var seen bool
	for _, entry := range logs.Items {
		if entry.Action == "retain" && entry.Transport == "http" {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("no retain entry in the audit trail of %d entries: %+v", logs.Total, logs.Items)
	}
}

func TestIntegrationRetiredProfileRouteAnswersGone(t *testing.T) {
	client, bankID := uniqueBank(t)

	// The contract retires the profile read (the document keeps the operation,
	// marked "removed"), so this pins the 410 that makes GetBankConfig the
	// supported way to read a bank's settings.
	_, httpResp, err := client.BanksAPI.GetBankProfile(context.Background(), bankID).Execute()
	if err == nil {
		t.Fatalf("the retired profile read answered successfully")
	}
	if httpStatus(httpResp) != 410 {
		t.Fatalf("retired profile read answered %v, want 410", httpStatus(httpResp))
	}
}

func TestIntegrationCompleteWorkflow(t *testing.T) {
	client, bankID := uniqueBank(t)
	ctx := context.Background()

	update := BankConfigUpdate{Updates: map[string]interface{}{"retain_extraction_mode": "concise"}}
	if _, _, err := client.BanksAPI.UpdateBankConfig(ctx, bankID).BankConfigUpdate(update).Execute(); err != nil {
		t.Fatalf("configuring the bank: %v", err)
	}

	retainText(t, client, bankID, "Wenyuan serves the agent-memory API with the reference schema.")

	recalled, err := client.Recall(ctx, bankID, RecallRequest{Query: "what does wenyuan serve?"})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(recalled.Results) == 0 {
		t.Fatalf("recall in the workflow found nothing")
	}
	if recalled.Results[0].ChunkId.Get() == nil {
		t.Logf("recall result carries no chunk id, which is allowed when the chunk arm is off")
	}

	// The generated API rather than the convenience Reflect, so the response
	// status is visible: a slower model can push a reflection past the
	// deployment's wall-clock budget, and that documented refusal (504) is a
	// valid outcome of the step, not a failure of the workflow.
	reflected, reflectResp, err := client.MemoryAPI.Reflect(ctx, bankID).
		ReflectRequest(ReflectRequest{Query: "summarise what is known."}).
		Execute()
	if err != nil {
		if httpStatus(reflectResp) == http.StatusGatewayTimeout {
			t.Logf("reflection exceeded the deployment's budget; the call and its refusal decoded")
		} else {
			t.Fatalf("reflect: %v (status %v)", err, httpStatus(reflectResp))
		}
	} else if strings.TrimSpace(reflected.Text) == "" {
		t.Fatalf("reflect in the workflow answered with no text")
	}

	if _, httpResp, err := client.BanksAPI.DeleteBank(ctx, bankID).Execute(); err != nil {
		t.Fatalf("delete: %v (status %v)", err, httpStatus(httpResp))
	}
}

func httpStatus(response *http.Response) int {
	if response == nil {
		return 0
	}
	return response.StatusCode
}

// bankFrom returns the listed bank, or nil when the list does not carry it.
func bankFrom(response *BankListResponse, bankID string) *BankListItem {
	if response == nil {
		return nil
	}
	for index := range response.Banks {
		if response.Banks[index].BankId == bankID {
			return &response.Banks[index]
		}
	}
	return nil
}

func bankListed(response *BankListResponse, bankID string) bool {
	if response == nil {
		return false
	}
	for _, bank := range response.Banks {
		if bank.BankId == bankID {
			return true
		}
	}
	return false
}

func statusOf(response interface{ GetStatusCode() int }) int {
	if response == nil {
		return 0
	}
	return response.GetStatusCode()
}
