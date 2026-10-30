package app

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/consolidation"
	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/tenant"
	"github.com/studyzy/wenyuan/internal/timefmt"
	"github.com/studyzy/wenyuan/internal/webhooks"
	"github.com/studyzy/wenyuan/internal/worker"
)

// archiveProcessor performs the queued transfer work. It reads the stashed
// upload through the stored-file facility and drives the existing import
// pipeline, so the worker owns the queue lifecycle while the archive format
// stays with the area that understands it. It also runs a queued consolidation
// pass, which needs the provider boundary for its model call.
type archiveProcessor struct {
	store    storage.Store
	provider provider.Provider
	// extraction carries the deployment's extraction knobs, so a queued write
	// extracts exactly as the synchronous one does.
	extraction ExtractionSettings
	// recallPerSourceCap bounds each retrieval arm of the consolidation
	// near-duplicate lookup; zero leaves it bound only by the pool budget.
	recallPerSourceCap int
	// mentalModelMinRefreshSeconds is the deployment's floor between two
	// automatic refreshes of one model, under each model's own trigger member.
	mentalModelMinRefreshSeconds int
	// reflection, when non-nil, makes a queued mental-model refresh write
	// prose through the model-driven loop rather than the deterministic
	// fallback.
	reflection storage.ReflectRunner
	// webhookGuard re-validates a delivery target and vets the address it
	// dials. It is the same guard the registration route enforces, so a URL
	// that was acceptable when stored is checked again against the policy in
	// force at delivery time. It also carries the deployment-level target, so
	// the delivery path and the enqueue path read one copy of it.
	webhookGuard *webhooks.Guard
	// discardDocumentText drops the text of the chunks an archive import
	// replays, which is the deployment's store_document_text setting. The
	// import's own chunks are replayed from the archive, and the retain write
	// path applies the same setting to a retained one.
	discardDocumentText bool
	// embeddings is the deployment's embedding model. The archive import
	// replays facts through the write path's embedding boundary, so an imported
	// fact is embedded with the target bank's model exactly as a retained one
	// is; a deployment with no embedding model leaves it nil.
	embeddings storage.EmbeddingGenerator
	// storeSchema is the tenant schema the worker serves (WENYUAN_API_DATABASE_SCHEMA).
	// The document export keys its archive under `tenants/{schema}/banks/...`,
	// the reference's own layout, so the published storage_key carries it.
	storeSchema string
}

// guard returns the delivery URL guard, defaulting to the strict policy when
// none was configured. A deployment that never opts into private targets must
// not deliver to one by accident.
func (p archiveProcessor) guard() *webhooks.Guard {
	if p.webhookGuard == nil {
		return webhooks.NewGuard(webhooks.GuardConfig{})
	}
	return p.webhookGuard
}

// logger returns the worker log sink.
func (p archiveProcessor) logger() *slog.Logger { return slog.Default() }

// Process runs one queued operation.
func (p archiveProcessor) Process(ctx context.Context, op storage.Operation) error {
	err := p.dispatch(ctx, op)
	// A provider rate limit is a transient condition: defer the operation
	// without consuming the retry budget rather than failing it.
	if err != nil && provider.IsRateLimitReset(err) {
		return worker.Defer(err)
	}
	if err != nil && consolidationFailed(op, err) {
		// A pass that did not finish reports to the same subscribers a completed
		// one reports to: the reference fires consolidation.completed with status
		// "failed" on every failure path — retryable, terminal and wall-clock
		// alike — and the event's data object then carries only the error.
		p.fireConsolidationEvent(ctx, op, "failed", map[string]any{"error_message": err.Error()})
		// The consolidation-only retry decision: the capped exponential ladder,
		// or a terminal failure when a peer consolidation already covers the bank.
		if retry, ok := p.consolidationRetry(ctx, op, err); ok {
			return retry
		}
	}
	return err
}

// consolidationRetryBackoffBase starts the consolidation retry ladder.
// The base is intentionally short so a momentary 5xx clears in seconds, not
// minutes; the cap keeps a genuine multi-hour outage from hammering the
// upstream (engine/memory_engine.py:525-533).
const (
	consolidationRetryBackoffBase = 5 * time.Second
	consolidationRetryBackoffMax  = 1800 * time.Second
)

// consolidationRetryBackoff is the capped exponential ladder the reference
// gives consolidation retries: 5, 10, 20, 40, 80, 160, 320, 640, 1280, then
// capped at 1800 (engine/memory_engine.py:529-533).
func consolidationRetryBackoff(retryCount int) time.Duration {
	backoff := consolidationRetryBackoffBase
	for range retryCount {
		backoff *= 2
		if backoff >= consolidationRetryBackoffMax {
			return consolidationRetryBackoffMax
		}
	}
	return backoff
}

// consolidationRetry decides how a failed consolidation pass is scheduled next:
// a transient failure retries indefinitely on the ladder above — exempt from
// the generic retry budget, whose exhaustion would silently dead-letter the
// bank's backlog — unless another consolidation is already pending for the
// bank. That pending op will process the same unconsolidated rows when it runs,
// so retrying this one just multiplies retry budgets during a long transient
// outage (every retain enqueues a fresh op): it is failed now, as the reference
// skips its retry there (engine/memory_engine.py:5165-5205). The second return
// reports whether the decision was made; a wall-clock cancellation is left to
// the consumer's own handling.
func (p archiveProcessor) consolidationRetry(ctx context.Context, op storage.Operation, cause error) (error, bool) {
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return nil, false
	}
	if p.hasOtherPendingConsolidation(ctx, op) {
		return worker.Permanent(cause), true
	}
	return worker.RetryAt(consolidationRetryBackoff(op.RetryCount), cause), true
}

// hasOtherPendingConsolidation reports whether another consolidation operation
// is already pending for the operation's bank. A check failure proceeds with
// the retry rather than swallowing a real failure, exactly as the reference's
// guard does (engine/memory_engine.py:22882-22917).
func (p archiveProcessor) hasOtherPendingConsolidation(ctx context.Context, op storage.Operation) bool {
	page, err := p.store.ListOperations(ctx, storage.OperationQuery{
		BankID: op.BankID,
		Status: string(storage.StatusPending),
		Type:   storage.OperationTypeConsolidate,
		Limit:  10,
	})
	if err != nil {
		return false
	}
	for _, pending := range page.Operations {
		if pending.OperationID != op.OperationID {
			return true
		}
	}
	return false
}

// consolidationFailed reports whether err ends a consolidation pass as a failure
// a subscriber should hear about. Only a consolidation reports this event, and
// two outcomes are not failures of the pass: a cancelled operation is not run at
// all (the reference skips it before its failure paths), and the no-model
// selection completes the pass rather than failing it, so neither fires an event.
func consolidationFailed(op storage.Operation, err error) bool {
	if op.OperationType != storage.OperationTypeConsolidate {
		return false
	}
	return !errors.Is(err, storage.ErrOperationAborted) && !errors.Is(err, consolidation.ErrNoModel)
}

// dispatch routes one operation to its handler.
func (p archiveProcessor) dispatch(ctx context.Context, op storage.Operation) error {
	// Every model call an operation's handler makes is recorded under that bank
	// and operation, so a trace listing shows the whole run rather than only the
	// calls whose own caller happened to attribute them.
	if operation, ok := traceOperationFor(op.OperationType); ok {
		ctx = provider.WithTraceContext(ctx, provider.TraceContext{BankID: op.BankID, Operation: operation})
	}
	switch op.OperationType {
	case storage.OperationTypeTransferImport, storage.OperationTypeDocumentImport:
		return p.applyImport(ctx, op)
	case storage.OperationTypeTransferExport, storage.OperationTypeDocumentExport:
		return p.applyExport(ctx, op)
	case storage.OperationTypeClone:
		return p.applyClone(ctx, op)
	case storage.OperationTypeConsolidate:
		return p.applyConsolidation(ctx, op)
	case storage.OperationTypeGraphMaintenance:
		return p.applyGraphMaintenance(ctx, op)
	case storage.OperationTypeRefreshMentalModel:
		return p.applyMentalModelRefresh(ctx, op)
	case storage.OperationTypeWebhookDelivery:
		return p.applyWebhookDelivery(ctx, op)
	case storage.OperationTypeRetain:
		return p.applyRetain(ctx, op)
	case storage.AsyncRetainType:
		// A batch parent carries no task payload: it is a status aggregator
		// whose lifecycle is driven entirely by its children, so there is
		// nothing to execute. The claim skips these rows, and reaching one here
		// means a row was written by something that does not; completing it
		// would report a batch finished while its children still run.
		return worker.Permanent(fmt.Errorf("batch parent %s is not executable", op.OperationID))
	default:
		// An unknown operation type is permanent: the same input would be
		// rejected again, so retrying only wastes the attempt budget.
		return worker.Permanent(fmt.Errorf("unsupported operation type %q", op.OperationType))
	}
}

// traceOperationFor names the trace operation one operation type records under.
// Types whose handler never calls a model have none, so their runs add no rows.
func traceOperationFor(operationType string) (string, bool) {
	switch operationType {
	case storage.OperationTypeRetain:
		return "retain", true
	case storage.OperationTypeConsolidate:
		return "consolidation", true
	case storage.OperationTypeRefreshMentalModel:
		return "refresh_mental_model", true
	default:
		return "", false
	}
}

// applyWebhookDelivery delivers one queued webhook event.
//
// A bank webhook's registration is read at delivery time, so an edit or a
// delete takes effect on the next attempt rather than on the next event. The
// deployment-level target has no registration to read: its URL and secret are
// carried on the queued delivery, because there is no row to look them up in.
// A 4xx response is permanent (the receiver rejects the shape); a transport
// failure or a 5xx is retried, and a since-deleted webhook is a permanent
// no-op.
func (p archiveProcessor) applyWebhookDelivery(ctx context.Context, op storage.Operation) error {
	eventType, _ := op.Payload["event_type"].(string)
	if eventType == "" {
		return worker.Permanent(errors.New("webhook delivery carries no event type"))
	}
	webhook, err := p.deliveryTarget(ctx, op)
	if err != nil {
		if errors.Is(err, storage.ErrWebhookNotFound) {
			return nil
		}
		return err
	}
	// The body was rendered when the event was raised, so the bytes sent are
	// the bytes the signature covers, whatever the payload has been through
	// since. A delivery queued by an older build without one is re-rendered,
	// under the event id the delivery carries so the two agree.
	body, _ := op.Payload["body"].(string)
	bodyBytes := []byte(body)
	if body == "" {
		payload, _ := op.Payload["data"].(map[string]any)
		rendered, err := marshalWebhookEvent(eventType, op.BankID, "", deliveryEventID(op), "completed", op.CreatedAt, payload)
		if err != nil {
			return worker.Permanent(err)
		}
		bodyBytes = rendered
	}
	status, responseBody, err := p.deliverWebhook(ctx, webhook, eventType, deliveryEventID(op), op.OperationID, bodyBytes)
	if err != nil {
		// A target the guard refuses at delivery time, or a receiver that
		// answers with a redirect, is a permanent failure: the same
		// registration fails the same way on every attempt, so retrying only
		// spends the attempt budget. The receiver's status is recorded when it
		// answered, so an operator can see what was refused.
		if errors.Is(err, storage.ErrWebhookURLNotAllowed) || errors.Is(err, errWebhookRedirectRefused) {
			worker.ReportOutcome(ctx, webhookOutcome(status, responseBody))
			return worker.Permanent(err)
		}
		// A transport failure is retried on the reference's backoff ladder, and
		// reported with no status because the receiver never answered.
		worker.ReportOutcome(ctx, webhookOutcome(0, ""))
		return webhookRetryOrFail(op, err)
	}
	// The receiver's status is recorded on the delivery either way: it is what
	// an operator reads to tell "the endpoint is refusing us" from "we never
	// reached it".
	worker.ReportOutcome(ctx, webhookOutcome(status, responseBody))
	if status >= 200 && status < 300 {
		return nil
	}
	// Every other status is retried on the ladder, the reference's behaviour: a
	// 4xx is not assumed to be permanent, so a receiver that is briefly
	// misconfigured gets the same retries a 5xx does. The delivery fails only
	// once the attempt budget is spent.
	return webhookRetryOrFail(op, webhookStatusFailure(status, webhook.URL, responseBody))
}

// deliveryEventID reads the id of the logical event the delivery belongs to. It
// stays the same across every retry (the queued payload is unchanged) and across
// every webhook the event fans out to; a delivery queued before event ids
// existed carries none, and then neither the body nor the header names one.
func deliveryEventID(op storage.Operation) string {
	eventID, _ := op.Payload["event_id"].(string)
	return eventID
}

// webhookRetryOrFail schedules one more attempt at the ladder's next rung, or
// fails the delivery permanently once the reference's attempts are spent. The
// reference makes MAX_ATTEMPTS = len(RETRY_DELAYS) + 1 attempts: the first try
// plus one per rung, so a retry_count that has reached the number of rungs is
// the last attempt.
func webhookRetryOrFail(op storage.Operation, cause error) error {
	if op.RetryCount >= len(webhookRetryDelays) {
		return worker.Permanent(cause)
	}
	return worker.RetryAt(webhookRetryDelay(op.RetryCount), cause)
}

// webhookStatusFailure renders a receiver's non-2xx answer as the reference does:
// the status, the endpoint, and the (bounded) body the receiver sent, which is
// the one place a receiver says why it refused.
func webhookStatusFailure(status int, url, responseBody string) error {
	body := responseBody
	if len(body) > webhookFailureBodyLimit {
		body = body[:webhookFailureBodyLimit]
	}
	return fmt.Errorf("HTTP %d from %s: %s", status, url, body)
}

// webhookFailureBodyLimit bounds the receiver body carried in a failure message,
// matching the reference's truncation.
const webhookFailureBodyLimit = 500

// deliveryTarget resolves the receiver one queued delivery addresses. A
// bank webhook is read from its live registration so an edit or a delete is
// honoured at delivery time; a global delivery carries its endpoint because it
// has no registration to read.
func (p archiveProcessor) deliveryTarget(ctx context.Context, op storage.Operation) (storage.Webhook, error) {
	if isGlobal, _ := op.Payload["global"].(bool); isGlobal {
		url, _ := op.Payload["url"].(string)
		if url == "" {
			return storage.Webhook{}, worker.Permanent(errors.New("global webhook delivery carries no url"))
		}
		secret, _ := op.Payload["secret"].(string)
		return storage.Webhook{URL: url, Secret: secret}, nil
	}
	webhookID, _ := op.Payload["webhook_id"].(string)
	if webhookID == "" {
		return storage.Webhook{}, worker.Permanent(errors.New("webhook delivery carries no webhook"))
	}
	return p.store.GetWebhook(ctx, op.BankID, webhookID)
}

// webhookOutcome renders the metadata recorded on one delivery attempt. The
// reference records the attempt time on every attempt and the receiver's
// response body (already bounded) whenever the receiver answered; whether the
// body is visible to a caller is decided when the delivery is read, not here.
func webhookOutcome(status int, responseBody string) map[string]any {
	outcome := map[string]any{
		"last_status_code": nullableStatus(status),
		"last_attempt_at":  httpx.FormatTimestamp(time.Now()),
	}
	if status != 0 {
		outcome["last_response_body"] = responseBody
	}
	return outcome
}

// deliveryHTTPConfig is the delivery shape a registration asks for, decoded
// from its stored http_config. A missing or unreadable member falls back to the
// reference's own defaults, so a webhook stored before a field existed still
// delivers.
type deliveryHTTPConfig struct {
	httpMethod string
	headers    map[string]string
	params     map[string]string
	timeout    time.Duration
}

// readDeliveryHTTPConfig reads the stored configuration. Every field is honoured
// the way the reference's delivery reads it: the method selects GET or POST,
// the headers and params are applied to the request, and the timeout bounds it.
func readDeliveryHTTPConfig(raw map[string]any) deliveryHTTPConfig {
	cfg := deliveryHTTPConfig{httpMethod: http.MethodPost, timeout: 30 * time.Second}
	if raw == nil {
		return cfg
	}
	if value, ok := raw["method"].(string); ok && strings.EqualFold(value, http.MethodGet) {
		cfg.httpMethod = http.MethodGet
	}
	if seconds, ok := raw["timeout_seconds"].(float64); ok && seconds > 0 {
		cfg.timeout = time.Duration(seconds) * time.Second
	}
	if seconds, ok := raw["timeout_seconds"].(int); ok && seconds > 0 {
		cfg.timeout = time.Duration(seconds) * time.Second
	}
	cfg.headers = stringMapFrom(raw["headers"])
	cfg.params = stringMapFrom(raw["params"])
	return cfg
}

// method returns the HTTP method the request must use. The reference only
// distinguishes GET from everything else, which is a POST.
func (c deliveryHTTPConfig) method() string {
	if c.httpMethod == http.MethodGet {
		return http.MethodGet
	}
	return http.MethodPost
}

// isGet reports whether the delivery is a bodyless GET.
func (c deliveryHTTPConfig) isGet() bool { return c.method() == http.MethodGet }

// stringMapFrom reads a stored string map, ignoring members that are not strings
// rather than failing the delivery.
func stringMapFrom(raw any) map[string]string {
	source, ok := raw.(map[string]any)
	if !ok {
		if direct, ok := raw.(map[string]string); ok {
			return direct
		}
		return nil
	}
	out := make(map[string]string, len(source))
	for key, value := range source {
		if text, ok := value.(string); ok {
			out[key] = text
		}
	}
	return out
}

// webhookRetryDelays is the delivery backoff ladder, matching the reference.
var webhookRetryDelays = []time.Duration{
	5 * time.Second, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 5 * time.Hour,
}

// webhookRetryDelay picks the delay for the given attempt, saturating at the
// last rung.
func webhookRetryDelay(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if attempt >= len(webhookRetryDelays) {
		attempt = len(webhookRetryDelays) - 1
	}
	return webhookRetryDelays[attempt]
}

// errWebhookRedirectRefused marks a delivery the receiver answered with a
// redirect. The guard checked the registered URL, not the hop, so following it
// would carry the method and body at a target the guard never vetted.
var errWebhookRedirectRefused = errors.New("webhook receiver answered with a redirect, which is not followed")

// nullableStatus renders a delivery status for the recorded outcome, or nil
// when the receiver never answered.
func nullableStatus(status int) any {
	if status == 0 {
		return nil
	}
	return status
}

// webhookResponseBodyLimit bounds the receiver response body a delivery records,
// matching the reference's truncation.
const webhookResponseBodyLimit = 2048

// deliverWebhook posts one signed delivery and returns the receiver's status and
// response body. The body is captured (bounded) on every attempt and whether it
// is visible to a caller is decided when the delivery is read. The signature is
// HMAC-SHA256 over "<timestamp>.<body>", keyed by the webhook's secret, carried
// in the reference's header shapes.
//
// The target is re-validated immediately before the request and the resolved
// address is vetted again at connect time, because a name can resolve
// differently at delivery than it did at registration. A redirect is refused
// rather than followed: it would move the request to a target the guard never
// checked.
//
// eventID identifies the logical event and stays the same on every retry and for
// every webhook the event goes to; deliveryID identifies this event's delivery to
// this webhook and stays the same on every retry. Both are service-controlled,
// like the event type and the signatures, so a configured header cannot set them.
func (p archiveProcessor) deliverWebhook(ctx context.Context, webhook storage.Webhook, eventType, eventID, deliveryID string, body []byte) (int, string, error) {
	guard := p.guard()
	if err := guard.Validate(webhook.URL); err != nil {
		return 0, "", err
	}
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	// The registration's http_config is the caller's delivery shape: only a GET
	// is a GET (any other method is a POST, as the reference decides), and its
	// headers, query parameters and timeout are the caller's.
	cfg := readDeliveryHTTPConfig(webhook.HTTPConfig)
	var payload io.Reader
	if !cfg.isGet() {
		payload = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, cfg.method(), webhook.URL, payload)
	if err != nil {
		return 0, "", worker.Permanent(err)
	}
	if len(cfg.params) > 0 {
		query := request.URL.Query()
		for name, value := range cfg.params {
			query.Set(name, value)
		}
		request.URL.RawQuery = query.Encode()
	}
	// The caller's headers go first so the service-controlled ones below
	// always win: a custom header must never be able to forge the event type or
	// overwrite a signature. Content-Type stays overridable, because some
	// receivers insist on a vendor media type and the body is JSON either way.
	request.Header.Set("Content-Type", "application/json")
	for name, value := range cfg.headers {
		request.Header.Set(name, value)
	}
	request.Header.Set("X-Hindsight-Event", eventType)
	// The id headers are set the way the reference sets them: only when they have
	// a value, because a delivery queued by an older build has no event id and the
	// reference skips a header it has nothing to put in rather than sending "".
	if eventID != "" {
		request.Header.Set("X-Hindsight-Event-Id", eventID)
	}
	if deliveryID != "" {
		request.Header.Set("X-Hindsight-Delivery-Id", deliveryID)
	}
	if webhook.Secret != "" {
		// The primary signature is over the raw body, `sha256=<hex>`, the
		// same construction GitHub uses. The conventional header name carries
		// the byte-identical value so a stock receiver verifies without a
		// reference-specific shim, and a timestamped variant lets a receiver
		// that checks freshness reject a replayed delivery.
		mac := hmac.New(sha256.New, []byte(webhook.Secret))
		mac.Write(body)
		signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		request.Header.Set("X-Hindsight-Signature", signature)
		request.Header.Set("X-Hub-Signature-256", signature)
		timed := hmac.New(sha256.New, []byte(webhook.Secret))
		timed.Write([]byte(timestamp + "." + string(body)))
		request.Header.Set("X-Hindsight-Signature-V2", "t="+timestamp+",v1="+hex.EncodeToString(timed.Sum(nil)))
	}
	client := &http.Client{
		Timeout:   cfg.timeout,
		Transport: p.webhookTransport(guard),
		// A redirect would carry the request to a location the guard never
		// saw; refusing here leaves the response to report, rather than
		// silently following up to ten hops.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errWebhookRedirectRefused
		},
	}
	response, err := client.Do(request)
	if err != nil {
		if response != nil {
			captured := readWebhookBody(response)
			_ = response.Body.Close()
			if errors.Is(err, errWebhookRedirectRefused) {
				return response.StatusCode, captured, errWebhookRedirectRefused
			}
		}
		return 0, "", err
	}
	defer func() { _ = response.Body.Close() }()
	return response.StatusCode, readWebhookBody(response), nil
}

// readWebhookBody captures the receiver's response body, bounded as the
// reference bounds it. The body is always captured because the reference
// persists it on every attempt and decides visibility when the delivery is
// read.
func readWebhookBody(response *http.Response) string {
	captured, _ := io.ReadAll(io.LimitReader(response.Body, webhookResponseBodyLimit))
	return string(captured)
}

// webhookTransport dials through the guard's address check. The dialer's
// control runs after a name is resolved and before the connection is made, so
// the address that is actually connected to is vetted: a name that resolved
// publicly at validation but privately at dial time cannot slip through.
func (p archiveProcessor) webhookTransport(guard *webhooks.Guard) *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	dialer := &net.Dialer{
		Timeout: 15 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			return guard.CheckResolvedAddress(host)
		},
	}
	transport.DialContext = dialer.DialContext
	return transport
}

// applyMentalModelRefresh regenerates one mental model from its source query.
//
// A refresh that cannot produce a document safe to store is a permanent
// refusal: the same query over the same memories would fail the same way, so
// retrying only spends the attempt budget. Recording the failure is what parks
// an automatic refresh, so a schedule that cannot succeed stops spending a
// model call on every tick.
func (p archiveProcessor) applyMentalModelRefresh(ctx context.Context, op storage.Operation) error {
	modelID, _ := op.Payload[storage.MentalModelIDPayloadKey].(string)
	if modelID == "" {
		return worker.Permanent(errors.New("refresh operation carries no mental model id"))
	}
	// An automatic refresh parks while the model's min-refresh window is open:
	// the operation goes back to pending with next_retry_at naming when the
	// window closes, no retry count and no error, so every trigger that fires
	// while it waits folds into it and the eventual run covers all of them
	// (memory_engine.py:4776-4846). The check reads state at execution time, so
	// a parked operation picks up a config change and measures against the
	// refresh that actually landed last.
	if automatic, _ := op.Payload[storage.AutomaticRefreshPayloadKey].(bool); automatic {
		if parked, parkErr := p.minRefreshPark(ctx, op.BankID, modelID); parkErr == nil && parked != nil {
			return worker.DeferAt(time.Until(parked.due), parked.reason)
		}
	}
	runner := newMentalModelRefresh(p.store, p.provider, p.reflection)
	// A "full refresh now" or a full_refresh_cron tick carries its mode in the
	// queued payload; it applies to this run only and never reaches the trigger.
	modeOverride, _ := op.Payload[storage.RefreshModePayloadKey].(string)
	_, report, err := runner.refresh(ctx, op.BankID, modelID, modeOverride)
	// The outcome is the only place a refusal is distinguishable from an empty
	// document: both leave the content as it was, and a refusal returns no
	// error. Log it, so "the document never changed" has a name in the log.
	slog.Default().Info("mental model refresh",
		"bank_id", op.BankID, "mental_model_id", modelID,
		"outcome", report.outcome, "failure_reason", report.failureReason)
	// Record what the refresh did on the operation, in the reference's
	// vocabulary: the outcome, why it refused to write, and the memory window it
	// read from. The consumer persists it when it settles the operation —
	// success and terminal failure alike — so an operator sees more than prose.
	worker.ReportOutcome(ctx, report.metadata())
	if err != nil {
		// Every failure leaves a history row, so a refused write is visible on
		// the model itself rather than only on the operation.
		recordMentalModelRefreshFailure(ctx, p.store, op.BankID, modelID, report, err)
		if transientRefreshFailure(err) {
			// The provider is down or rate-limited; it will answer later, so the
			// model is not parked. The operation still fails, so a caller sees
			// the failure, but the next automatic trigger retries it.
			return worker.Permanent(err)
		}
		if permanentRefreshFailure(err) || provider.IsAuthRejected(err) {
			if recordErr := p.store.RecordMentalModelRefreshFailure(ctx, op.BankID, modelID, time.Now().UTC()); recordErr != nil {
				// A failure that cannot be recorded is still the failure that
				// matters; the operation's own error carries the reason.
				p.logger().Warn("could not record mental model refresh failure",
					"bank_id", op.BankID, "mental_model_id", modelID, "error", recordErr)
			}
			return worker.Permanent(err)
		}
		return err
	}
	return nil
}

// transientRefreshFailure reports whether a failed refresh failed because the
// provider was temporarily unavailable — a rate limit, a 5xx, a timeout — rather
// than because the prompt itself cannot be answered.
//
// The distinction decides whether the model is parked: a doomed prompt would
// fail the same way forever, but a provider that is merely down will answer
// later, and parking on it would freeze the model for days after the limit had
// already reset.
// minRefreshPark decides whether an automatic refresh must wait out the
// model's min-refresh window. It resolves the interval the way the reference
// does — the model's own trigger.min_refresh_interval_seconds, including an
// explicit 0, over the bank's setting over the deployment's default, with a
// non-numeric value ignored rather than raised on — and answers the parked
// deferral when the window is still open (memory_engine.py:4734-4846).
func (p archiveProcessor) minRefreshPark(ctx context.Context, bankID, modelID string) (*parkedRefresh, error) {
	model, err := p.store.GetMentalModel(ctx, bankID, modelID)
	if err != nil || model.ID == "" {
		// A model that vanished fails its refresh with the message it has
		// always failed with; the defer check contributes nothing.
		return nil, nil
	}
	interval, resolved := p.minRefreshIntervalSeconds(ctx, bankID, model.Trigger)
	if !resolved || interval <= 0 || model.LastRefreshedAt.IsZero() {
		return nil, nil
	}
	due := model.LastRefreshedAt.Add(time.Duration(interval) * time.Second)
	now := time.Now().UTC()
	if !now.Before(due) {
		return nil, nil
	}
	return &parkedRefresh{
		due: due,
		reason: fmt.Errorf("min_refresh_interval_seconds=%d for mental model %s: last refreshed at %s, next refresh due at %s",
			interval, modelID, model.LastRefreshedAt.UTC().Format(time.RFC3339Nano), due.UTC().Format(time.RFC3339Nano)),
	}, nil
}

// minRefreshIntervalSeconds resolves the floor for one model's automatic
// refreshes, reporting whether a floor applies at all.
func (p archiveProcessor) minRefreshIntervalSeconds(ctx context.Context, bankID string, trigger map[string]any) (int, bool) {
	if perModel, present := trigger["min_refresh_interval_seconds"]; present && perModel != nil {
		switch value := perModel.(type) {
		case float64:
			return max(0, int(value)), true
		case int:
			return max(0, value), true
		default:
			slog.Default().Warn("Ignoring non-numeric trigger.min_refresh_interval_seconds; falling back to the bank setting",
				"value", perModel)
		}
	}
	if overrides, err := p.store.BankConfig(ctx, bankID); err == nil && overrides != nil {
		if fromBank, present := overrides["mental_model_min_refresh_interval_seconds"]; present && fromBank != nil {
			switch value := fromBank.(type) {
			case float64:
				return max(0, int(value)), true
			case int:
				return max(0, value), true
			default:
				slog.Default().Warn("Ignoring non-numeric mental_model_min_refresh_interval_seconds; no minimum refresh interval will be applied",
					"value", fromBank)
				return 0, false
			}
		}
	}
	if p.mentalModelMinRefreshSeconds > 0 {
		return p.mentalModelMinRefreshSeconds, true
	}
	return 0, false
}

// parkedRefresh carries the deferral an open min-refresh window produces.
type parkedRefresh struct {
	due    time.Time
	reason error
}

func transientRefreshFailure(err error) bool {
	return provider.IsTransient(err) || provider.IsRateLimitReset(err)
}

// recordMentalModelRefreshFailure appends the history row every refused refresh
// leaves, so the model's History shows what happened rather than the last
// successful run as though it were current.
func recordMentalModelRefreshFailure(ctx context.Context, store storage.Store, bankID, modelID string, report mentalModelRefreshReport, cause error) {
	entry := storage.MentalModelHistoryEntry{
		ChangedAt:     time.Now().UTC(),
		Kind:          storage.MentalModelHistoryFailureKind,
		Outcome:       report.outcome,
		FailureReason: report.failureReason,
		ErrorMessage:  cause.Error(),
	}
	if err := store.RecordMentalModelHistory(ctx, bankID, modelID, entry); err != nil {
		slog.Default().Warn("could not record mental model refresh history",
			"bank_id", bankID, "mental_model_id", modelID, "error", err)
	}
}

// applyConsolidation runs one consolidation pass over the operation's bank. A
// bank with no model configured is a permanent refusal rather than a retry,
// because the same request would be refused again.
func (p archiveProcessor) applyConsolidation(ctx context.Context, op storage.Operation) error {
	consolidator := consolidation.New(p.provider, consolidationStore{store: p.store, perSourceCap: p.recallPerSourceCap})
	if !consolidator.Available() {
		return worker.Permanent(consolidation.ErrNoModel)
	}
	// A chain an explicit /consolidate started keeps draining past the
	// auto-consolidation gate: the route's enqueue carries caller_requested in
	// the task payload and the pass reads it from there
	// (memory_engine.py:4697, :23716-23717).
	if requested, ok := op.Payload["caller_requested"].(bool); ok && requested {
		consolidator.WithCallerRequested(true)
	}
	// The pass reports its phases to the operation running it, so a poller sees
	// the work advancing; a store without the capability simply runs unreported.
	consolidator.WithOperation(op.OperationID, p.operationProgressReporter())
	if result, err := consolidator.Run(ctx, op.BankID); err != nil {
		if errors.Is(err, consolidation.ErrNoModel) {
			return worker.Permanent(err)
		}
		// A rejected credential is a function of the configured key, so the
		// pass cannot succeed on replay; fail it once rather than walking the
		// retry ladder (#5644).
		if provider.IsAuthRejected(err) {
			return worker.Permanent(err)
		}
		return err
	} else {
		// A completed consolidation is an event a webhook may subscribe to. The
		// counts are the reference's ConsolidationEventData, so a subscriber
		// sees what the pass did rather than only that it finished.
		p.fireConsolidationEvent(ctx, op, "completed", map[string]any{
			"observations_created": result.Created,
			"observations_updated": result.Updated,
			"observations_deleted": result.Deleted,
		})
		// A page that follows consolidation refreshes once the pass it was
		// waiting on has landed, so the new observations are what it reads.
		p.enqueueConsolidationRefreshes(ctx, op.BankID)
	}
	return nil
}

// applyGraphMaintenance runs the graph-maintenance sweep a curation edit
// submitted: it drains the bank's relink and entity-prune queues through the
// same drain the periodic graph.maintenance job runs, so the curated unit's
// neighbours are relinked and its orphaned entities are pruned. A store with no
// maintenance queue has nothing to drain, so the operation is a no-op there.
func (p archiveProcessor) applyGraphMaintenance(ctx context.Context, op storage.Operation) error {
	maintainer, ok := p.store.(storage.GraphMaintainer)
	if !ok {
		return nil
	}
	return drainGraphMaintenanceInSchema(ctx, maintainer)
}

// enqueueConsolidationRefreshes queues a refresh for every model that follows
// consolidation and has actually moved.
//
// The staleness check is what keeps a consolidation round from spending a model
// call to regenerate content that would be identical, and the park check is what
// stops a model whose last refresh failed from being re-queued on every round
// (its failure already had its worker retries; re-sending the same prompt only
// pays for the same failure).
func (p archiveProcessor) enqueueConsolidationRefreshes(ctx context.Context, bankID string) {
	models, err := p.store.ListMentalModels(ctx, bankID, "")
	if err != nil {
		slog.Default().Warn("could not list mental models for consolidation refresh",
			"bank_id", bankID, "error", err)
		return
	}
	for _, model := range models {
		if !triggerRefreshAfterConsolidation(model.Trigger) {
			continue
		}
		if refreshPaused(model) {
			continue
		}
		if !p.store.IsStale(ctx, bankID, model) {
			continue
		}
		if _, err := p.store.EnqueueOperation(ctx, storage.EnqueueRequest{
			Operation: storage.Operation{
				BankID:        bankID,
				OperationType: storage.OperationTypeRefreshMentalModel,
				Payload: map[string]any{
					storage.MentalModelIDPayloadKey:    model.ID,
					storage.AutomaticRefreshPayloadKey: true,
				},
				SerializationKey: storage.MentalModelSerializationKey(model.ID),
			},
			DedupeByPayloadKey:       storage.MentalModelIDPayloadKey,
			DedupeIncludesProcessing: true,
		}); err != nil {
			slog.Default().Warn("could not queue a consolidation refresh",
				"bank_id", bankID, "mental_model_id", model.ID, "error", err)
		}
	}
}

// triggerRefreshAfterConsolidation reports whether a trigger follows
// consolidation.
func triggerRefreshAfterConsolidation(trigger map[string]any) bool {
	refresh, _ := trigger["refresh_after_consolidation"].(bool)
	return refresh
}

// refreshPaused reports whether a model's last refresh failed and has not been
// superseded by a success, which parks its automatic refreshes.
func refreshPaused(model storage.MentalModel) bool {
	if model.LastRefreshFailedAt == nil {
		return false
	}
	return model.LastRefreshedAt.IsZero() || model.LastRefreshFailedAt.After(model.LastRefreshedAt)
}

// enqueueWebhookEvent queues a delivery for every enabled webhook of the bank
// that subscribes to the event, plus the deployment-level target when it
// subscribes. A registration is read at delivery time, so the queued work
// carries only the identity and the event.
func (p archiveProcessor) enqueueWebhookEvent(ctx context.Context, bankID, eventType string, data map[string]any) {
	enqueueWebhookEvent(ctx, p.store, p.guard().Global(), bankID, eventType, data)
}

// fireConsolidationEvent queues one consolidation.completed event: either the
// counts a completed pass applied, or the error that stopped a failed one. Both
// outcomes go through one emitter, so a subscriber sees a single event shape and
// the two branches cannot drift. The status and the operation go into the
// envelope; a member the caller leaves out is absent from the rendered object,
// which is how the reference's event model drops an unset count on the failure
// path.
func (p archiveProcessor) fireConsolidationEvent(ctx context.Context, op storage.Operation, status string, data map[string]any) {
	data["operation_id"] = op.OperationID
	data["status"] = status
	p.enqueueWebhookEvent(ctx, op.BankID, "consolidation.completed", data)
}

// enqueueWebhookEvent is the one place a webhook delivery is created: both the
// worker's own events and the memoryops Memory Defense notifier go through it,
// so a delivery has a single shape and a single subscription check.
//
// The event envelope is built here, once, and stored on the queued delivery:
// the body is what a receiver verifies the signature over, so it must be the
// exact bytes that go on the wire, not a re-serialization at delivery time.
// Two reserved keys steer the envelope — `operation_id` and `status` — and are
// taken out of the event data so they do not also appear inside it. A member
// whose value is nil is left out of the rendered object, which is how the
// reference's `exclude_none` serialization treats an unset member: a caller
// that means "this event does not report that" passes no key rather than a
// placeholder value.
//
// The deployment-level target is delivered alongside the bank's own webhooks
// and independent of them: it is one static receiver named by the deployment,
// so it has no row to read and its endpoint and secret are carried on the
// queued delivery.
//
// One event id is minted here, once, and carried on every delivery the event
// fans out to, so a receiver can dedupe on it: `operation_id` cannot serve,
// because a batch retain fires one event per document under one operation.
func enqueueWebhookEvent(ctx context.Context, store storage.Store, global *webhooks.GlobalTarget, bankID, eventType string, data map[string]any) {
	registrations, err := store.ListWebhooks(ctx, bankID)
	if err != nil {
		slog.Default().Warn("could not read webhooks for an event", "bank_id", bankID, "error", err)
		return
	}
	payloadData := make(map[string]any, len(data))
	for key, value := range data {
		payloadData[key] = value
	}
	operationID, _ := payloadData["operation_id"].(string)
	delete(payloadData, "operation_id")
	status, _ := payloadData["status"].(string)
	if status == "" {
		status = "completed"
	}
	delete(payloadData, "status")
	eventID := webhooks.NewEventID()
	body, err := marshalWebhookEvent(eventType, bankID, operationID, eventID, status, time.Now().UTC(), payloadData)
	if err != nil {
		slog.Default().Warn("could not render a webhook event", "bank_id", bankID, "event_type", eventType, "error", err)
		return
	}
	if global.Enabled() && global.Subscribes(eventType) {
		_, err := store.EnqueueOperation(ctx, storage.EnqueueRequest{
			Operation: storage.Operation{
				BankID:        bankID,
				OperationType: storage.OperationTypeWebhookDelivery,
				Payload: map[string]any{
					"global":     true,
					"event_id":   eventID,
					"event_type": eventType,
					"url":        global.URL,
					"secret":     global.Secret,
					"body":       string(body),
					"data":       payloadData,
				},
				SerializationKey: "webhook:global:" + bankID,
			},
		})
		if err != nil {
			slog.Default().Warn("could not enqueue a global webhook delivery",
				"bank_id", bankID, "error", err)
		}
	}
	for _, webhook := range registrations {
		if !webhook.Enabled || !subscribesTo(webhook.EventTypes, eventType) {
			continue
		}
		_, err := store.EnqueueOperation(ctx, storage.EnqueueRequest{
			Operation: storage.Operation{
				BankID:        bankID,
				OperationType: storage.OperationTypeWebhookDelivery,
				Payload: map[string]any{
					"webhook_id": webhook.ID,
					"event_id":   eventID,
					"event_type": eventType,
					"url":        webhook.URL,
					"body":       string(body),
					"data":       payloadData,
				},
				SerializationKey: "webhook:" + webhook.ID,
			},
		})
		if err != nil {
			slog.Default().Warn("could not enqueue a webhook delivery",
				"bank_id", bankID, "webhook_id", webhook.ID, "error", err)
		}
	}
}

// webhookEventBody is the wire payload of one delivery, in the reference's
// WebhookEvent field order. The order matters: a receiver that recomputes the
// signature from the body it parsed re-serializes in this same order, so the
// bytes stay identical.
//
// EventID leads because the reference's field leads the model; it is the one
// member omitted when empty, since a task queued before event ids existed has
// none and the reference's default-factory member is then simply absent (see
// marshalWebhookEvent).
type webhookEventBody struct {
	EventID     string          `json:"event_id,omitempty"`
	Event       string          `json:"event"`
	BankID      string          `json:"bank_id"`
	OperationID string          `json:"operation_id"`
	Status      string          `json:"status"`
	Timestamp   string          `json:"timestamp"`
	Data        json.RawMessage `json:"data"`
}

// webhookEventDataOrder is the declaration order of each event's data members.
// The reference builds the object from a pydantic model and serializes it with
// `model_dump_json(exclude_none=True)`, so members come out in declaration
// order and a member the emitter left unset is absent from the wire rather than
// present as null. Both properties are part of what a receiver hashes.
var webhookEventDataOrder = map[string][]string{
	"consolidation.completed": {"observations_created", "observations_updated", "observations_deleted", "error_message"},
	"retain.completed":        {"document_id", "tags", "memory_unit_count"},
	"memory_defense.triggered": {
		"action", "detector", "document_id", "matched_types", "message",
		// The SIEM-enrichment members an extension may populate; the built-in
		// defense leaves them unset, so they never reach the wire.
		"severity", "api_key_name", "hits", "memory_unit_id", "receipt_uri",
	},
}

// marshalWebhookEvent renders the event envelope as compact JSON with no HTML
// escaping, so a receiver's re-serialization of the parsed body reproduces these
// exact bytes and the signature still verifies.
//
// An empty eventID is left out rather than rendered as "": it is the one member
// with a default and no `exclude_none` exception, so the reference's model
// always carries one — the only body without it is the one a delivery re-renders
// for a task queued before event ids existed.
func marshalWebhookEvent(eventType, bankID, operationID, eventID, status string, at time.Time, data map[string]any) ([]byte, error) {
	encoded, err := marshalWebhookData(eventType, data)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(webhookEventBody{
		EventID:     eventID,
		Event:       eventType,
		BankID:      bankID,
		OperationID: operationID,
		Status:      status,
		// The event body is a pydantic model in the reference, so its
		// timestamp takes the pydantic JSON form.
		Timestamp: timefmt.FormatZuluTimestamp(at),
		Data:      encoded,
	}); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// marshalWebhookData renders one event's data object the way the reference's
// model does: the members the event type declares, in declaration order, with
// every unset (null) member dropped. A member the declaration does not name is
// emitted after the declared ones in sorted order, so a payload a caller adds
// is never silently lost.
func marshalWebhookData(eventType string, data map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	written := make(map[string]bool, len(data))
	first := true
	write := func(key string, value any) error {
		if value == nil {
			// exclude_none: an unset member is absent, not null.
			return nil
		}
		member, err := encodeJSONMember(value)
		if err != nil {
			return err
		}
		keyJSON, err := json.Marshal(key)
		if err != nil {
			return err
		}
		if !first {
			buf.WriteByte(',')
		}
		first = false
		buf.Write(keyJSON)
		buf.WriteByte(':')
		buf.Write(member)
		return nil
	}
	for _, key := range webhookEventDataOrder[eventType] {
		value, present := data[key]
		if !present {
			continue
		}
		written[key] = true
		if err := write(key, value); err != nil {
			return nil, err
		}
	}
	extra := make([]string, 0, len(data))
	for key := range data {
		if !written[key] {
			extra = append(extra, key)
		}
	}
	sort.Strings(extra)
	for _, key := range extra {
		if err := write(key, data[key]); err != nil {
			return nil, err
		}
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// encodeJSONMember renders one data member under the envelope's escaping rules.
func encodeJSONMember(value any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// webhookNotifier adapts the worker's enqueue path to the capability interface
// the memoryops handlers hold, so an event raised on a request path creates a
// delivery exactly the way every other event does.
type webhookNotifier struct {
	store storage.Store
	// guard carries the deployment-level target delivered alongside each
	// bank's own registrations. A nil guard (or one with no global target)
	// means the feature is off.
	guard *webhooks.Guard
}

// EnqueueWebhookEvent queues one delivery per subscribed webhook of the bank,
// plus the deployment-level target when it subscribes.
func (n webhookNotifier) EnqueueWebhookEvent(ctx context.Context, bankID, eventType string, data map[string]any) {
	enqueueWebhookEvent(ctx, n.store, n.guard.Global(), bankID, eventType, data)
}

// subscribesTo reports whether an event type is in a webhook's subscription.
func subscribesTo(eventTypes []string, eventType string) bool {
	for _, subscribed := range eventTypes {
		if subscribed == eventType {
			return true
		}
	}
	return false
}

// consolidationStore adapts the storage area to the consolidation engine's
// narrow interface, translating the engine's plain value types into the storage
// records. Keeping the translation here lets the engine stay free of storage
// types and the storage area stay free of the engine.
type consolidationStore struct {
	store storage.Store
	// perSourceCap bounds each retrieval arm fed to the near-duplicate lookup,
	// so one over-expanding arm cannot crowd the others out of the pool.
	perSourceCap int
}

// ConsolidationFacts maps the storage facts into the engine's value type.
func (a consolidationStore) ConsolidationFacts(ctx context.Context, bankID string, limit int) ([]consolidation.Fact, error) {
	records, err := a.store.ConsolidationFacts(ctx, bankID, limit)
	if err != nil {
		return nil, err
	}
	facts := make([]consolidation.Fact, 0, len(records))
	for _, record := range records {
		facts = append(facts, consolidation.Fact{
			ID: record.ID, Text: record.Text,
			OccurredStart: formatOptionalTime(record.OccurredStart),
			OccurredEnd:   formatOptionalTime(record.OccurredEnd),
			MentionedAt:   formatOptionalTime(record.MentionedAt),
			Tags:          record.Tags, ObservationScopes: record.ObservationScopes,
		})
	}
	return facts, nil
}

// ConsolidationObservations maps the storage observations of one scope into the
// engine's value type. A store that implements the scoped capability reads only
// the scope's observations; a store that does not has its full pool filtered
// here, so the two backends agree on which observations a pass may merge into.
func (a consolidationStore) ConsolidationObservations(ctx context.Context, bankID string, scope []string, limit int) ([]consolidation.Observation, error) {
	if scoped, ok := a.store.(storage.ConsolidationScopeStore); ok {
		records, err := scoped.ConsolidationObservationsInScope(ctx, bankID, scope, limit)
		if err != nil {
			return nil, err
		}
		return mapObservations(records), nil
	}
	records, err := a.store.ConsolidationObservations(ctx, bankID, limit)
	if err != nil {
		return nil, err
	}
	mapped := mapObservations(records)
	kept := mapped[:0]
	for _, observation := range mapped {
		if sameObservationScope(observation.Scope, scope) {
			kept = append(kept, observation)
		}
	}
	return kept, nil
}

// mapObservations translates storage observations into the engine's value type.
func mapObservations(records []storage.ConsolidationObservation) []consolidation.Observation {
	observations := make([]consolidation.Observation, 0, len(records))
	for _, record := range records {
		observations = append(observations, consolidation.Observation{
			ID: record.ID, Text: record.Text, ProofCount: record.ProofCount,
			SourceIDs: record.SourceIDs, SourceTexts: record.SourceTexts,
			MentionedAt: formatOptionalTime(record.MentionedAt),
			Scope:       record.Scope,
			Embedding:   record.Embedding,
		})
	}
	return observations
}

// sameObservationScope reports whether two scopes name the same tag set.
func sameObservationScope(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	left := append([]string(nil), a...)
	right := append([]string(nil), b...)
	sort.Strings(left)
	sort.Strings(right)
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// CreateObservation records a new observation at its scope, using the store's
// scoped capability when it has one.
func (a consolidationStore) CreateObservation(ctx context.Context, bankID, text string, sourceIDs, scope []string) (string, error) {
	if scoped, ok := a.store.(storage.ConsolidationScopeStore); ok {
		return scoped.CreateObservationInScope(ctx, bankID, text, sourceIDs, scope)
	}
	return a.store.CreateObservation(ctx, bankID, text, sourceIDs)
}

// UpdateObservation rewrites an observation.
func (a consolidationStore) UpdateObservation(ctx context.Context, bankID, observationID, text string, sourceIDs []string) error {
	return a.store.UpdateObservation(ctx, bankID, observationID, text, sourceIDs)
}

// DeleteObservation removes an observation.
func (a consolidationStore) DeleteObservation(ctx context.Context, bankID, observationID string) error {
	return a.store.DeleteObservation(ctx, bankID, observationID)
}

// MarkConsolidated records that the given facts have been folded.
func (a consolidationStore) MarkConsolidated(ctx context.Context, bankID string, factIDs []string) error {
	return a.store.MarkConsolidated(ctx, bankID, factIDs)
}

// MarkConsolidationFailed records that a pass gave up on the given facts.
func (a consolidationStore) MarkConsolidationFailed(ctx context.Context, bankID string, factIDs []string) error {
	return a.store.MarkConsolidationFailed(ctx, bankID, factIDs)
}

// MergeObservationEvidence folds a merged-away observation's evidence into its
// survivor — or requeues that evidence — the bookkeeping the reference's
// _fold_deleted_evidence does (consolidator.py:3335-3380). A store without the
// capability leaves every merge a plain delete.
func (a consolidationStore) MergeObservationEvidence(ctx context.Context, bankID, survivorID, mergedID string) error {
	if merger, ok := a.store.(interface {
		MergeObservationEvidence(ctx context.Context, bankID, survivorID, mergedID string) error
	}); ok {
		return merger.MergeObservationEvidence(ctx, bankID, survivorID, mergedID)
	}
	return nil
}

// EnqueueConsolidationSuccessor requeues the next round of a round-capped
// consolidation chain. A store without the capability leaves the pass stopping
// silently at its round budget.
func (a consolidationStore) EnqueueConsolidationSuccessor(ctx context.Context, bankID string, callerRequested bool) error {
	if enqueuer, ok := a.store.(interface {
		EnqueueConsolidationSuccessor(ctx context.Context, bankID string, callerRequested bool) error
	}); ok {
		return enqueuer.EnqueueConsolidationSuccessor(ctx, bankID, callerRequested)
	}
	return nil
}

// AppendDocumentEvents files the pass's document change-history events against
// the operation, so GET .../documents/{id}/history rebuilds them. Without this
// forward the consolidation pass's own history.EventWriter assertion never
// matches and the events are silently dropped.
func (a consolidationStore) AppendDocumentEvents(ctx context.Context, opID string, events []map[string]any) error {
	return a.store.AppendDocumentEvents(ctx, opID, events)
}

// CountObservationsInScope reports the scope's observation total with no limit,
// so the scope cap is measured against the true count rather than the bounded
// candidate pool (the reference's count-to-the-end fix). A store without the
// capability leaves the cap measured against the pool read.
func (a consolidationStore) CountObservationsInScope(ctx context.Context, bankID string, scope []string) (int, error) {
	if counter, ok := a.store.(interface {
		CountObservationsInScope(ctx context.Context, bankID string, scope []string) (int, error)
	}); ok {
		return counter.CountObservationsInScope(ctx, bankID, scope)
	}
	return 0, nil
}

// BankConfig returns the bank's raw configuration overrides, which carry the
// consolidation settings and strategies the pass reads.
func (a consolidationStore) BankConfig(ctx context.Context, bankID string) (map[string]any, error) {
	return a.store.BankConfig(ctx, bankID)
}

// ConsolidationTuning reports the deployment's consolidation knobs, so the pass
// runs with the configured load batch size, model-call budget and attempt
// budget instead of the package defaults. A store that does not carry them
// leaves the pass on the reference's shipped values.
func (a consolidationStore) ConsolidationTuning() storage.ConsolidationTuning {
	if tuned, ok := a.store.(interface {
		ConsolidationTuning() storage.ConsolidationTuning
	}); ok {
		return tuned.ConsolidationTuning()
	}
	return storage.DefaultConsolidationTuning()
}

// ObservationDuplicates embeds a candidate observation text and returns that
// embedding together with the in-scope observations carrying a stored vector,
// so the consolidation pass can decide near-duplicates by cosine similarity.
// It reports embedded=false when the store cannot probe — a deployment with no
// embedding model — which leaves the pass on its text-similarity floor.
func (a consolidationStore) ObservationDuplicates(ctx context.Context, bankID string, scope []string, text string, limit int) ([]float32, []consolidation.Observation, bool, error) {
	prober, ok := a.store.(interface {
		ObservationDuplicates(ctx context.Context, bankID string, scope []string, text string, limit int) ([]float32, []storage.ConsolidationObservation, bool, error)
	})
	if !ok {
		return nil, nil, false, nil
	}
	probe, records, embedded, err := prober.ObservationDuplicates(ctx, bankID, scope, text, limit)
	if err != nil {
		return nil, nil, embedded, err
	}
	return probe, mapObservations(records), embedded, nil
}

// RelatedObservations looks up the observations that resemble the facts being
// folded, one recall per fact text.
//
// The recall runs in interleave fusion on purpose: summing reciprocal ranks
// averages down a candidate that is the best hit of one arm but absent from the
// others, which is exactly the near-identical twin this lookup exists to find.
// Only the observation type is retrieved, and the results are de-duplicated
// across the lookups.
func (a consolidationStore) RelatedObservations(ctx context.Context, bankID string, scope []string, texts []string, limit int) ([]consolidation.Observation, error) {
	tuning := a.ConsolidationTuning()
	budget := tuning.RecallBudget
	if budget == "" {
		budget = storage.DefaultConsolidationRecallBudget
	}
	perQuery := limit
	if perQuery <= 0 {
		perQuery = 5
	}
	// The lookup is restricted to the pass's scope so a differently-scoped
	// observation can never be offered as a merge candidate. A non-empty scope
	// matches under "all_strict" (the observation carries every scope tag and
	// is not untagged); the empty scope leaves the recall unscoped and the
	// engine drops anything that is not actually untagged.
	var tags []string
	tagsMatch := "any"
	if len(scope) > 0 {
		tags = scope
		tagsMatch = "all_strict"
	}
	seen := map[string]bool{}
	related := make([]consolidation.Observation, 0, limit)
	for _, text := range texts {
		if len(related) >= limit {
			break
		}
		result, err := a.store.Recall(ctx, storage.RecallQuery{
			BankID:       bankID,
			Query:        text,
			Types:        []string{string(storage.TypeObservation)},
			Fusion:       storage.FusionInterleave,
			PerSourceCap: a.perSourceCap,
			Limit:        perQuery,
			Budget:       budget,
			MaxTokens:    tuning.MaxTokens,
			Tags:         tags,
			TagsMatch:    tagsMatch,
		})
		if err != nil {
			return nil, err
		}
		for _, match := range result.Matches {
			if match.Type != string(storage.TypeObservation) || seen[match.ID] {
				continue
			}
			seen[match.ID] = true
			sources := match.ObservationSourceIDs
			if len(sources) == 0 {
				sources = match.SourceMemoryIDs
			}
			related = append(related, consolidation.Observation{
				ID: match.ID, Text: match.Text, ProofCount: len(sources),
				SourceIDs: sources, MentionedAt: formatOptionalTime(match.MentionedAt),
				Scope: match.Tags,
			})
			if len(related) >= limit {
				break
			}
		}
	}
	return related, nil
}

// formatOptionalTime renders an optional timestamp for the model's input,
// leaving an unknown time empty rather than substituting a misleading one.
func formatOptionalTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

// NewQueueConsumer builds the queue consumer over the store, translating the
// resolved worker configuration into the consumer's tuning. The guard is the
// deployment's webhook URL policy; it is enforced again at delivery time, not
// only when a target was registered. The embeddings generator is the model an
// archive import re-embeds with, exactly as the retain write path does.
func NewQueueConsumer(store storage.Store, model provider.Provider, reflection storage.ReflectRunner,
	embeddings storage.EmbeddingGenerator, cfg config.WorkerConfig, tuning config.WorkerTuning,
	guard *webhooks.Guard, extraction ExtractionSettings, discardDocumentText bool,
	storeSchema string, tenants tenant.Extension) *worker.Consumer {
	return worker.New(store, archiveProcessor{
		store: store, provider: model, reflection: reflection,
		recallPerSourceCap:           cfg.RecallMaxCandidatesPerSource,
		mentalModelMinRefreshSeconds: tuning.MentalModelMinRefreshSeconds,
		webhookGuard:                 guard,
		embeddings:                   embeddings,
		extraction:                   extraction,
		discardDocumentText:          discardDocumentText,
		storeSchema:                  storeSchema,
	}, worker.Config{
		WorkerID:            cfg.ID,
		MaxRetries:          cfg.MaxRetries,
		InitialBackoff:      cfg.InitialBackoff,
		MaxBackoff:          cfg.MaxBackoff,
		Lease:               cfg.Lease,
		Concurrency:         cfg.Concurrency,
		PollInterval:        cfg.PollInterval,
		WallTimeouts:        cfg.WallTimeouts,
		ReservedSlots:       cfg.ReservedSlots,
		BackpressureDefer:   tuning.BackpressureDefer,
		ConsolidationByBank: tuning.ConsolidationByBank,
		Tenants:             tenants,
	})
}

// RunQueueConsumer drains the queue until the context ends, treating a
// cancelled context as a clean stop rather than a failure.
func RunQueueConsumer(ctx context.Context, consumer *worker.Consumer) error {
	if err := consumer.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// operationProgressReporter builds the consolidator's progress sink: it writes
// each snapshot to the operation's record through the store's own writer. The
// write is a heartbeat, so a failure is logged and dropped rather than failing
// the job it reports on — the reference's `_write_operation_progress` swallows
// its errors for the same reason (`memory_engine.py:5156-5178`).
func (p archiveProcessor) operationProgressReporter() consolidation.ProgressReporter {
	writer, ok := p.store.(storage.OperationProgressWriter)
	if !ok {
		return nil
	}
	return func(operationID string, snapshot map[string]any) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := writer.RecordOperationProgress(ctx, operationID, snapshot); err != nil {
			slog.Debug("record operation progress", "operation", operationID, "error", err)
		}
	}
}
