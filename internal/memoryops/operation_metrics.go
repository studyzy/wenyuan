package memoryops

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/studyzy/wenyuan/internal/monitoring"
)

// The reference records one operation observation per served call, under the
// `source="api"` label the worker does not use (api/http.py:6243 for recall,
// :6442 for reflect, :10279 for the synchronous retain). The counters are what
// an operator reads to see request-level throughput and failure rate, so the
// label set has to be the reference's rather than a database-level guess.
//
// Two properties of the reference's context manager matter here. It records on
// the way out for both outcomes, so a failure counts as `success="false"` rather
// than vanishing; and it excludes a client disconnect entirely (metrics.py
// `record_operation` catches the cancellation and sets `cancelled`), because an
// abandoned request is neither a success nor a failure and counting it as one
// would bias the success rate. `serveOperation` below mirrors both.
const (
	// operationSourceAPI is the reference's source label for work a client asked
	// the HTTP API to do.
	operationSourceAPI = "api"

	operationRecall  = "recall"
	operationReflect = "reflect"
	operationRetain  = "retain"
)

// serveOperation runs one operation body and records its outcome.
//
// A nil error that is a cancelled context is a client disconnect: the reference
// drops that request from the counters, so it is not recorded here either. Every
// other outcome — success or failure — is recorded exactly once, with the
// operation's own labels.
func serveOperation(ctx context.Context, operation, budget, maxTokens string, run func() error) error {
	started := time.Now()
	err := run()
	disconnected := ctx.Err() != nil && (err == nil || errors.Is(err, context.Canceled))
	if !disconnected {
		monitoring.Workload.ObserveOperation(operation, operationSourceAPI,
			budget, maxTokens, err == nil, time.Since(started).Seconds())
	}
	return err
}

// recallOperationLabels returns the recall operation's budget and max_tokens
// labels. The reference labels the series with the request's own values, so an
// omitted budget reads as the model's default ("mid") and an omitted max_tokens
// as its default 4096 — a request that never named them is still answered with
// them. A count supplied by the caller is rendered decimally, including the
// negative one that asks for no facts: the reference passes the value straight
// through, so `max_tokens: -5` yields `max_tokens="-5"` rather than an empty
// label.
func recallOperationLabels(req RecallRequest) (budget, maxTokens string) {
	return budgetOrDefault(req.Budget), strconv.Itoa(recallMaxTokens(req))
}

// reflectOperationLabels returns the reflect operation's budget label. A
// reflection carries no token budget of its own, so its max_tokens label is
// empty, as the reference's series shows.
func reflectOperationLabels(req ReflectRequest) (budget, maxTokens string) {
	return resolveReflectBudget(req.Budget, nil), ""
}

// retainOperationLabels returns the retain operation's labels: a synchronous
// retain takes neither a budget nor a token count, so both are empty.
func retainOperationLabels() (budget, maxTokens string) { return "", "" }
