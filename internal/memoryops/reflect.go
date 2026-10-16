package memoryops

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
)

// defaultReflectBudget is the effort level applied when neither the request nor
// the bank sets one.
const defaultReflectBudget = storage.ReflectBudgetLow

// defaultReflectSearchObservationsMaxTokens is the shipped per-bank default for
// the observation-search token budget.
const defaultReflectSearchObservationsMaxTokens = 0

// defaultReflectSearchObservationsIncludeEntities is the shipped per-bank
// default for entity attachment on observation evidence.
const defaultReflectSearchObservationsIncludeEntities = false

// ReflectDefaultOptions is the deployment's process-level default for a bank's
// reflect_default_options object: the reflect options applied wherever neither
// the request nor the bank's own value sets the matching field, e.g.
// {"reflect_search_observations_max_tokens": 3000}. The composition root wires
// it from HINDSIGHT_API_REFLECT_DEFAULT_OPTIONS; nil means the deployment set
// nothing, so every option keeps its shipped default. The bank's own
// reflect_default_options always wins over it.
var ReflectDefaultOptions map[string]any

// reflectDefaultOptionInt reads an integer option out of ReflectDefaultOptions,
// accepting the int and float64 a JSON object decodes to. A missing key or a
// value of the wrong shape reports not-set, so a malformed entry cannot take
// the place of the shipped default.
func reflectDefaultOptionInt(key string) (int, bool) {
	switch value := ReflectDefaultOptions[key].(type) {
	case int:
		return value, true
	case int64:
		return int(value), true
	case float64:
		return int(value), true
	default:
		return 0, false
	}
}

// reflectDefaultOptionBool reads a boolean option out of
// ReflectDefaultOptions. A missing key or a value of the wrong shape reports
// not-set.
func reflectDefaultOptionBool(key string) (bool, bool) {
	value, ok := ReflectDefaultOptions[key].(bool)
	return value, ok
}

// Synthesis produces reflection text from retrieval context. The foundation
// unit fixes the response contract; synthesis quality is delegated.
type Synthesis interface {
	Synthesize(req ReflectRequest, ctx storage.ReflectContext) (ReflectResponse, error)
}

// Reflect serves the reflection route: one synchronous request-and-response on
// a bank-scoped path. There is no streaming variant.
func (h *Handlers) Reflect(w http.ResponseWriter, r *http.Request) {
	bankID := r.PathValue("bank_id")
	var req ReflectRequest
	body, err := decodeJSON(r, &req)
	if err != nil {
		httpx.WriteValidationFor(w, r, httpx.BodyDecodeFailure(err, body))
		return
	}
	if failure := ValidateReflect(req, h.ReflectSchemaValidator); failure != nil {
		httpx.WriteValidationFor(w, r, failure)
		return
	}
	query := h.buildReflectQuery(bankID, req)
	ctx, cancel := reflectTimeoutContext(r, h.ReflectTimeout)
	defer cancel()
	// Every model call the reflection makes is recorded under this bank and
	// operation, so a trace listing shows the run rather than only the calls
	// that happened to be attributed by their caller.
	ctx = provider.WithTraceContext(ctx, provider.TraceContext{BankID: bankID, Operation: "reflect"})
	outcome, err := h.Store.Reflect(ctx, query)
	if err != nil {
		// A wall-clock budget that elapsed is a gateway timeout; a client
		// disconnect cancels cooperatively and is not misreported.
		if errors.Is(err, context.DeadlineExceeded) {
			err = storage.ErrReflectTimeout
		}
		if errors.Is(err, context.Canceled) {
			return
		}
		httpx.WriteErrorFor(w, r, h.mapReflectError(err))
		return
	}
	response, err := buildReflectResponse(req, outcome)
	if err != nil {
		httpx.WriteErrorFor(w, r, h.mapReflectError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, response)
}

// buildReflectQuery resolves a validated request into the storage reflect
// query, appending the deprecated context field and resolving the two per-bank
// defaults as explicit-request over bank default over shipped default.
func (h *Handlers) buildReflectQuery(bankID string, req ReflectRequest) storage.ReflectQuery {
	include := storage.ReflectInclude{}
	if req.Include != nil {
		if req.Include.Facts != nil {
			include.Facts = &storage.ReflectFactsInclude{MaxTokens: req.Include.Facts.MaxTokens}
		}
		if req.Include.ToolCalls != nil {
			include.ToolCalls = true
			include.ToolOutput = req.Include.ToolCalls.Output
		}
	} else if req.Trace {
		include.ToolCalls = true
	}
	query := storage.ReflectQuery{
		BankID:                            bankID,
		Query:                             appendContext(req.Query, req.Context),
		Budget:                            resolveReflectBudget(req.Budget, h.BankReflectDefaults),
		HasBudget:                         req.Budget != "",
		MaxTokens:                         derefInt(req.MaxTokens),
		Include:                           include,
		ResponseSchema:                    req.ResponseSchema,
		Types:                             req.FactTypes,
		ExcludeMentalModels:               req.ExcludeMentalModels,
		ExcludeMentalModelIDs:             req.ExcludeMentalModelIDs,
		Tags:                              req.Tags,
		TagsMatch:                         req.TagsMatch,
		TagGroups:                         toStorageTagGroups(req.TagGroups.Groups),
		ApplyAllDirectives:                req.ApplyAllDirectives,
		SearchObservationsMaxTokens:       resolveObservationsMaxTokens(req, h.BankReflectDefaults),
		SearchObservationsIncludeEntities: resolveObservationsIncludeEntities(req, h.BankReflectDefaults),
		Timeout:                           h.ReflectTimeout,
		Reflection:                        h.Reflection,
	}
	if h.ReflectCatalog != nil {
		query.MentalModels = h.ReflectCatalog.MentalModels(bankID)
		query.Directives = h.ReflectCatalog.Directives(bankID)
	}
	return query
}

// resolveReflectBudget resolves the effort level as explicit-request over bank
// default over shipped default.
func resolveReflectBudget(request string, bank *BankReflectDefaults) string {
	if request != "" {
		return request
	}
	if bank != nil && bank.Budget != "" {
		return bank.Budget
	}
	return defaultReflectBudget
}

// resolveObservationsMaxTokens resolves the observation-search token budget as
// explicit-request over bank default over the deployment's reflect-default
// options over shipped default.
func resolveObservationsMaxTokens(req ReflectRequest, bank *BankReflectDefaults) int {
	if req.ReflectSearchObservationsMaxTokens != nil {
		return *req.ReflectSearchObservationsMaxTokens
	}
	if bank != nil && bank.SearchObservationsMaxTokens != nil {
		return *bank.SearchObservationsMaxTokens
	}
	if value, ok := reflectDefaultOptionInt("reflect_search_observations_max_tokens"); ok {
		return value
	}
	return defaultReflectSearchObservationsMaxTokens
}

// resolveObservationsIncludeEntities resolves the entity-attachment flag as
// explicit-request over bank default over the deployment's reflect-default
// options over shipped default.
func resolveObservationsIncludeEntities(req ReflectRequest, bank *BankReflectDefaults) bool {
	if req.ReflectSearchObservationsIncludeEntities != nil {
		return *req.ReflectSearchObservationsIncludeEntities
	}
	if bank != nil && bank.SearchObservationsIncludeEntities != nil {
		return *bank.SearchObservationsIncludeEntities
	}
	if value, ok := reflectDefaultOptionBool("reflect_search_observations_include_entities"); ok {
		return value
	}
	return defaultReflectSearchObservationsIncludeEntities
}

// appendContext appends the deprecated context field onto the query. A blank
// context leaves the query unchanged; a non-blank context makes a blank query
// acceptable.
func appendContext(query, context string) string {
	if context == "" {
		return query
	}
	if query == "" {
		return context
	}
	return query + "\n" + context
}

// reflectTimeoutContext applies the reflection wall-clock budget to the request
// context, and cancels cooperatively when the client disconnects.
func reflectTimeoutContext(r *http.Request, budget time.Duration) (context.Context, context.CancelFunc) {
	if budget <= 0 {
		return r.Context(), func() {}
	}
	return context.WithTimeout(r.Context(), budget)
}

// mapReflectError maps a storage reflection failure onto the public status
// contract.
func (h *Handlers) mapReflectError(err error) error {
	switch {
	case errors.Is(err, storage.ErrBankNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank)
	case errors.Is(err, storage.ErrReadShed):
		return httpx.NewHTTPError(httpx.KindUnavailable, "reflection is temporarily unavailable").
			WithHeader(httpx.HeaderRetryAfter, itoa(h.DefaultRetryAfterSeconds))
	case errors.Is(err, storage.ErrReflectTimeout):
		detail := err.Error()
		if errors.Is(err, storage.ErrReflectTimeout) && err.Error() == storage.ErrReflectTimeout.Error() {
			detail = "reflection exceeded its wall-clock budget; try a smaller budget or a simpler query"
		}
		return httpx.NewHTTPError(httpx.KindTimeout, detail)
	case errors.Is(err, storage.ErrReflectModelUnavailable):
		return httpx.NewHTTPError(httpx.KindBadRequest, err.Error())
	case errors.Is(err, storage.ErrReflectNoAnswer), errors.Is(err, storage.ErrReflectToolDriving),
		errors.Is(err, storage.ErrReflectRetrieval):
		return httpx.NewHTTPError(httpx.KindInternal, err.Error())
	default:
		return httpx.NewHTTPError(httpx.KindInternal, httpx.DetailInternal)
	}
}
