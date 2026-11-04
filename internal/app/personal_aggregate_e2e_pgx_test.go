//go:build pgx

package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/tenant"
)

// The aggregate end to end: the served handler, the real store, the real job.
//
// The in-memory and job tests pin the decisions; these pin what a client sees —
// that a write straight into `<tenant>::myself` is accepted and then replaced by
// the next round, that the aggregate is readable like any bank, and that the
// management plane reports it. They also pin the property the projection exists
// for: consolidation reads the aggregate's own rows and never stamps a source
// bank's fact.

// aggregateE2EFixture is one provisioned tenant with its key and a served app.
type aggregateE2EFixture struct {
	db       storage.DB
	app      *App
	tenantID string
	key      string
	// model is the stub the app was composed with. The projection calls no model;
	// consolidation of the projected rows does, so it must report a usable name.
	model provider.Provider
}

// newAggregateE2EFixture provisions a tenant in a multi-tenant deployment.
func newAggregateE2EFixture(t *testing.T, model provider.Provider) *aggregateE2EFixture {
	t.Helper()
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the aggregate e2e test was not run")
	}
	db, err := storage.OpenDatabaseWithBudget(url, storage.PoolBudget{MaxOpenConns: 6, MaxIdleConns: 2})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	tenantID := fmt.Sprintf("t_agg_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_, _ = db.ExecContext(cleanupCtx, "DROP SCHEMA IF EXISTS \""+tenantID+"\" CASCADE")
		_, _ = db.ExecContext(cleanupCtx, "DELETE FROM admin.tenant WHERE tenant_id = $1", tenantID)
		_ = db.Close()
	})

	cfg := testConfig()
	cfg.TenantMode = config.TenantModeMulti
	cfg.AdminListen = "127.0.0.1:0"
	cfg.AdminAPIKey = "operator-secret"
	cfg.RunMigrationsOnStartup = true
	store := storage.NewMultiTenantPostgresStore(db, "public")
	application, err := New(context.Background(), cfg, Dependencies{Store: store, Provider: model})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	key := issueTenantKey(t, application.AdminHandler, tenantID)
	return &aggregateE2EFixture{db: db, app: application, tenantID: tenantID, key: key, model: model}
}

// call issues one data-plane request with the tenant's key.
func (f *aggregateE2EFixture) call(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+f.key)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	f.app.Handler.ServeHTTP(recorder, request)
	return recorder
}

// admin issues one management request with the operator key.
func (f *aggregateE2EFixture) admin(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return adminCall(t, f.app.AdminHandler, method, path, body)
}

// ctx is the tenant's schema context.
func (f *aggregateE2EFixture) ctx(t *testing.T) context.Context {
	t.Helper()
	ctx, err := storage.WithSchema(context.Background(), f.tenantID)
	if err != nil {
		t.Fatalf("WithSchema: %v", err)
	}
	return ctx
}

// runRound runs the real projection job once.
func (f *aggregateE2EFixture) runRound(t *testing.T) {
	t.Helper()
	runner := &personalRunner{
		store:  storage.NewPostgresPersonalStore(f.db),
		models: func(string) provider.Provider { return f.model },
		cfg:    rollupTestCfg(),
	}
	if err := runner.runPersonalRounds(context.Background()); err != nil {
		t.Fatalf("round: %v", err)
	}
}

// writeFact puts one world fact into a bank the way a retain does once it has
// extracted.
//
// The bank is created through the store rather than with an inserted row: a bank
// exists once EnsureBank has made it, and a bare row would leave every
// bank-scoped read answering not-found.
func (f *aggregateE2EFixture) writeFact(t *testing.T, bank, text string) string {
	t.Helper()
	return f.writeFactOfKind(t, bank, text, "world")
}

// writeFactOfKind puts one fact of the given kind into a bank.
func (f *aggregateE2EFixture) writeFactOfKind(t *testing.T, bank, text, kind string) string {
	t.Helper()
	bankID := tenant.ProjectBankID(f.tenantID, bank)
	if err := f.app.Store.EnsureBank(f.ctx(t), bankID); err != nil {
		t.Fatalf("ensure bank %s: %v", bankID, err)
	}
	var id string
	if err := f.db.QueryRowContext(f.ctx(t), `
		INSERT INTO "`+f.tenantID+`".memory_units (bank_id, text, fact_type)
		VALUES ($1,$2,$3) RETURNING id::text`, bankID, text, kind).Scan(&id); err != nil {
		t.Fatalf("insert fact: %v", err)
	}
	return id
}

// unmanagedFact puts one world fact into a bank without creating the bank row,
// which is what a client that retained before the bank existed leaves behind: the
// aggregate's own id has no bank until a round creates one.
func (f *aggregateE2EFixture) unmanagedFact(t *testing.T, bank, text string) string {
	t.Helper()
	bankID := tenant.ProjectBankID(f.tenantID, bank)
	var id string
	if err := f.db.QueryRowContext(f.ctx(t), `
		INSERT INTO "`+f.tenantID+`".memory_units (bank_id, text, fact_type)
		VALUES ($1,$2,'world') RETURNING id::text`, bankID, text).Scan(&id); err != nil {
		t.Fatalf("insert unmanaged fact: %v", err)
	}
	return id
}

// aggregate is the tenant's aggregate bank id.
func (f *aggregateE2EFixture) aggregate() string { return tenant.PersonalBankID(f.tenantID) }

// extractedFact is what a stub extraction yields for any retained text.
const extractedFact = "orders deploys on port 15432."

// TestPostgresAggregateRetainIntoMySelfIsAccepted is the product decision at the
// data plane's edge: a client may retain into `<tenant>::myself`, so nothing in
// the served surface refuses it. The write is answered, and the aggregate bank
// exists afterwards with the fact in it.
func TestPostgresAggregateRetainIntoMySelfIsAccepted(t *testing.T) {
	model := &rollupOnlyProvider{answerProvider: &answerProvider{name: "fake"}}
	f := newAggregateE2EFixture(t, model)

	direct := f.call(t, http.MethodPost, "/v1/default/banks/"+f.aggregate()+"/memories",
		`{"items":[{"content":"a conclusion someone wrote by hand"}],"async":false}`)
	if direct.Code == http.StatusForbidden || direct.Code == http.StatusMethodNotAllowed {
		t.Fatalf("a write into the aggregate was refused with %d: %s", direct.Code, direct.Body.String())
	}
	// Whatever the pipeline made of the text, it reached the bank rather than the
	// edge refusing the bank id.
	var banks int
	if err := f.db.QueryRowContext(f.ctx(t), `SELECT count(*) FROM "`+f.tenantID+`".banks WHERE bank_id = $1`,
		f.aggregate()).Scan(&banks); err != nil {
		t.Fatalf("count banks: %v", err)
	}
	if banks != 1 {
		t.Fatalf("the aggregate bank does not exist after a write to it")
	}
}

// rollupOnlyProvider reports the extraction path as unusable, which is what a
// deployment without an extraction-capable model looks like: retain then fails
// at the model, never at the bank id. The projection calls no model at all.
type rollupOnlyProvider struct{ *answerProvider }

func (p *rollupOnlyProvider) Complete(ctx context.Context, req provider.CompletionRequest) (provider.CompletionResult, error) {
	if req.ResponseSchema != nil {
		return provider.CompletionResult{}, errors.New("no extraction model in this test")
	}
	return p.answerProvider.Complete(ctx, req)
}

// TestPostgresAggregateProjectionReplacesADirectlyWrittenRow pins the content
// ownership end to end: a row in the aggregate bank that no round projected is
// replaced by the next round, and the source bank's fact is projected verbatim.
func TestPostgresAggregateProjectionReplacesADirectlyWrittenRow(t *testing.T) {
	model := &rollupOnlyProvider{answerProvider: &answerProvider{name: "fake"}}
	f := newAggregateE2EFixture(t, model)
	f.writeFact(t, "orders", extractedFact)
	f.unmanagedFact(t, "myself", "a conclusion someone wrote by hand")

	f.runRound(t)

	list := f.call(t, http.MethodGet, "/v1/default/banks/"+f.aggregate()+"/memories/list?limit=100", "")
	if list.Code != http.StatusOK {
		t.Fatalf("list aggregate = %d: %s", list.Code, list.Body.String())
	}
	body := list.Body.String()
	if !strings.Contains(body, extractedFact) {
		t.Fatalf("the aggregate does not carry the projected fact verbatim: %s", body)
	}
	if strings.Contains(body, "written by hand") {
		t.Fatalf("a directly written row survived the round: %s", body)
	}

	// The list the console shows is the bank list, which resolves banks through
	// the banks table: the aggregate must be in it, not merely addressable.
	banks := f.call(t, http.MethodGet, "/v1/default/banks?limit=100", "")
	if banks.Code != http.StatusOK {
		t.Fatalf("list banks = %d: %s", banks.Code, banks.Body.String())
	}
	var listed struct {
		Banks []struct {
			BankID string `json:"bank_id"`
		} `json:"banks"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(banks.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode banks: %v", err)
	}
	found := false
	for _, bank := range listed.Banks {
		if bank.BankID == f.aggregate() {
			found = true
		}
	}
	if !found {
		t.Fatalf("the aggregate bank is missing from the bank list: %+v", listed.Banks)
	}
}

// TestPostgresAggregateIsReadableAndManaged pins the two surfaces: the aggregate
// reads like any bank through the data plane, and the management plane reports it.
func TestPostgresAggregateIsReadableAndManaged(t *testing.T) {
	model := &answerProvider{name: "fake"}
	f := newAggregateE2EFixture(t, model)

	f.writeFact(t, "orders", extractedFact)
	f.runRound(t)

	// The source bank's own reads are unchanged by the aggregate's existence.
	sourceList := f.call(t, http.MethodGet, "/v1/default/banks/"+tenant.ProjectBankID(f.tenantID, "orders")+"/memories/list?limit=100", "")
	if sourceList.Code != http.StatusOK {
		t.Fatalf("list source = %d: %s", sourceList.Code, sourceList.Body.String())
	}
	if !strings.Contains(sourceList.Body.String(), extractedFact) {
		t.Fatalf("the source bank lost its own fact: %s", sourceList.Body.String())
	}

	status := f.admin(t, http.MethodGet, "/admin/v1/tenants/"+f.tenantID+"/aggregate", "")
	if status.Code != http.StatusOK {
		t.Fatalf("aggregate status = %d: %s", status.Code, status.Body.String())
	}
	var decoded struct {
		BankID         string `json:"bank_id"`
		Aggregate      bool   `json:"aggregate"`
		AggregateFacts int    `json:"aggregate_facts"`
		SourceBanks    int    `json:"source_banks"`
	}
	if err := json.Unmarshal(status.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if !decoded.Aggregate || decoded.BankID != f.aggregate() {
		t.Fatalf("status = %+v, want the tenant's aggregate", decoded)
	}
	if decoded.AggregateFacts != 1 {
		t.Fatalf("aggregate facts = %d, want 1", decoded.AggregateFacts)
	}

	// The operator's reset and rebuild levers answer.
	if reset := f.admin(t, http.MethodPost, "/admin/v1/tenants/"+f.tenantID+"/aggregate/reset", ""); reset.Code != http.StatusOK {
		t.Fatalf("reset = %d: %s", reset.Code, reset.Body.String())
	}
	if rebuild := f.admin(t, http.MethodPost, "/admin/v1/tenants/"+f.tenantID+"/aggregate/rebuild", ""); rebuild.Code != http.StatusOK {
		t.Fatalf("rebuild = %d: %s", rebuild.Code, rebuild.Body.String())
	}
}

// TestPostgresAggregateProjectsWorldAndExperience pins the input to the aggregate:
// both kinds of source fact are projected, verbatim, from every bank but myself.
func TestPostgresAggregateProjectsWorldAndExperience(t *testing.T) {
	model := &answerProvider{name: "fake"}
	f := newAggregateE2EFixture(t, model)
	f.writeFactOfKind(t, "orders", extractedFact, "world")
	f.writeFactOfKind(t, "billing", "billing stores amounts in integer cents", "experience")
	f.writeFactOfKind(t, "myself", "a directly written row", "world")

	f.runRound(t)

	var world, experience int
	if err := f.db.QueryRowContext(f.ctx(t), `
		SELECT count(*) FILTER (WHERE fact_type='world'),
		       count(*) FILTER (WHERE fact_type='experience')
		FROM "`+f.tenantID+`".memory_units WHERE bank_id=$1`, f.aggregate()).Scan(&world, &experience); err != nil {
		t.Fatalf("count kinds: %v", err)
	}
	if world != 1 || experience != 1 {
		t.Fatalf("world/experience = %d/%d, want one of each", world, experience)
	}
	var direct int
	if err := f.db.QueryRowContext(f.ctx(t), `
		SELECT count(*) FROM "`+f.tenantID+`".memory_units
		WHERE bank_id=$1 AND text='a directly written row'`, f.aggregate()).Scan(&direct); err != nil {
		t.Fatalf("count direct row: %v", err)
	}
	if direct != 0 {
		t.Fatal("a directly written row survived the projection")
	}
	// Every projected row carries the metadata that marks it as the job's own,
	// and the source banks' rows are untouched.
	var unmarked int
	if err := f.db.QueryRowContext(f.ctx(t), `
		SELECT count(*) FROM "`+f.tenantID+`".memory_units
		WHERE bank_id=$1 AND fact_type IN ('world','experience')
		  AND COALESCE(metadata->>'projection_key','') = ''`, f.aggregate()).Scan(&unmarked); err != nil {
		t.Fatalf("count unmarked: %v", err)
	}
	if unmarked != 0 {
		t.Fatalf("unmarked projected rows = %d, want none", unmarked)
	}
	var sourceStamped int
	if err := f.db.QueryRowContext(f.ctx(t), `
		SELECT count(*) FROM "`+f.tenantID+`".memory_units
		WHERE bank_id <> $1 AND fact_type IN ('world','experience') AND consolidated_at IS NOT NULL`,
		f.aggregate()).Scan(&sourceStamped); err != nil {
		t.Fatalf("count source stamps: %v", err)
	}
	if sourceStamped != 0 {
		t.Fatalf("source facts stamped consolidated = %d, want none", sourceStamped)
	}
}

// TestPostgresAggregateConsolidatesItsOwnRows is the property the projection
// exists for. A consolidation pass over the aggregate bank folds its projected
// world and experience rows into an observation, stamps those rows
// consolidated, and leaves every source bank's own facts completely untouched.
func TestPostgresAggregateConsolidatesItsOwnRows(t *testing.T) {
	// The consolidation model answers with one observation resting on every fact
	// it is shown; those ids are the aggregate's own projected rows, never a
	// source bank's facts.
	model := &decisionProvider{text: "orders deploys on port 15432 and billing stores cents"}
	f := newAggregateE2EFixture(t, model)
	f.writeFactOfKind(t, "orders", extractedFact, "world")
	f.writeFactOfKind(t, "billing", "billing stores amounts in integer cents", "experience")

	f.runRound(t)

	processor := archiveProcessor{store: f.app.Store, provider: model}
	if err := processor.Process(f.ctx(t), storage.Operation{
		OperationID: "op-agg-consolidate", BankID: f.aggregate(),
		OperationType: storage.OperationTypeConsolidate,
	}); err != nil {
		t.Fatalf("consolidation: %v", err)
	}

	var observations int
	if err := f.db.QueryRowContext(f.ctx(t), `
		SELECT count(*) FROM "`+f.tenantID+`".memory_units
		WHERE bank_id=$1 AND fact_type='observation'`, f.aggregate()).Scan(&observations); err != nil {
		t.Fatalf("count observations: %v", err)
	}
	if observations != 1 {
		t.Fatalf("observations = %d, want one built from the projected rows", observations)
	}
	// The projected rows, and only those, are stamped consolidated.
	var aggregateStamped, sourceStamped int
	if err := f.db.QueryRowContext(f.ctx(t), `
		SELECT count(*) FILTER (WHERE bank_id=$1),
		       count(*) FILTER (WHERE bank_id<>$1)
		FROM "`+f.tenantID+`".memory_units
		WHERE fact_type IN ('world','experience') AND consolidated_at IS NOT NULL`,
		f.aggregate()).Scan(&aggregateStamped, &sourceStamped); err != nil {
		t.Fatalf("count stamps: %v", err)
	}
	if aggregateStamped != 2 {
		t.Fatalf("stamped aggregate rows = %d, want both projected rows", aggregateStamped)
	}
	if sourceStamped != 0 {
		t.Fatalf("stamped source rows = %d, want none: a source bank must not be touched", sourceStamped)
	}
}

// factIDPattern reads the "[uuid]" prefix buildInput puts on each new fact.
var factIDPattern = regexp.MustCompile(`\[([0-9a-fA-F-]{36})\]`)

// decisionProvider answers one completion with a single observation resting on
// every fact the pass showed it. Citing all the fact ids keeps the pass from
// re-asking facts a response left unaddressed, which would create a second
// observation for the same batch.
type decisionProvider struct {
	text  string
	calls int
}

func (p *decisionProvider) Name() string                           { return "openai" }
func (p *decisionProvider) Dimensions() int                        { return provider.DeclaredDimensions() }
func (p *decisionProvider) VerifyConnection(context.Context) error { return nil }
func (p *decisionProvider) Embed(context.Context, provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	return provider.EmbeddingResult{}, nil
}
func (p *decisionProvider) Complete(_ context.Context, req provider.CompletionRequest) (provider.CompletionResult, error) {
	p.calls++
	var user string
	for _, message := range req.Messages {
		if message.Role == "user" {
			user = message.Content
		}
	}
	ids := make([]string, 0)
	for _, match := range factIDPattern.FindAllStringSubmatch(user, -1) {
		ids = append(ids, match[1])
	}
	decision := map[string]any{
		"creates": []any{map[string]any{
			"text": p.text, "source_fact_ids": ids, "reason": "one durable conclusion",
		}},
		"updates": []any{},
		"deletes": []any{},
	}
	encoded, err := json.Marshal(decision)
	if err != nil {
		return provider.CompletionResult{}, err
	}
	return provider.CompletionResult{Content: string(encoded)}, nil
}

// TestPostgresAggregateReportsATeamAsHavingNone pins the product decision on the
// management plane: a team is not a person.
func TestPostgresAggregateReportsATeamAsHavingNone(t *testing.T) {
	model := &answerProvider{name: "fake"}
	f := newAggregateE2EFixture(t, model)
	teamID := f.tenantID + "_team"
	created := f.admin(t, http.MethodPost, "/admin/v1/teams", fmt.Sprintf(`{"tenant_id":%q}`, teamID))
	if created.Code != http.StatusCreated {
		t.Fatalf("create team = %d: %s", created.Code, created.Body.String())
	}
	// A team with one member, so the page has a member's aggregate to carry.
	if added := f.admin(t, http.MethodPut, "/admin/v1/teams/"+teamID+"/members/"+f.tenantID, `{"role":"member"}`); added.Code != http.StatusOK {
		t.Fatalf("add team member = %d: %s", added.Code, added.Body.String())
	}
	t.Cleanup(func() {
		_, _ = f.db.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS \""+teamID+"\" CASCADE")
		_, _ = f.db.ExecContext(context.Background(), "DELETE FROM admin.tenant WHERE tenant_id = $1", teamID)
	})
	status := f.admin(t, http.MethodGet, "/admin/v1/tenants/"+teamID+"/aggregate", "")
	if status.Code != http.StatusOK {
		t.Fatalf("aggregate status = %d: %s", status.Code, status.Body.String())
	}
	var decoded struct {
		Aggregate bool   `json:"aggregate"`
		Kind      string `json:"kind"`
		Reason    string `json:"reason"`
		Members   []struct {
			TenantID string `json:"tenant_id"`
		} `json:"members"`
	}
	if err := json.Unmarshal(status.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if decoded.Aggregate || decoded.Kind != "team" || decoded.Reason == "" {
		t.Fatalf("status = %+v, want a team with no aggregate and a reason", decoded)
	}
	// The team's own banks are nobody's aggregate, but its members' are theirs.
	if len(decoded.Members) != 1 || decoded.Members[0].TenantID != f.tenantID {
		t.Fatalf("team members = %+v, want the one member", decoded.Members)
	}
}
