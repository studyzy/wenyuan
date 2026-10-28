package monitoring

import (
	"sort"
	"strconv"
	"strings"
	"sync"
)

// The per-workload Prometheus family names, in the exposition spelling the
// reference's OpenTelemetry exporter produces from its instrument names: the
// name's dots become underscores, a counter's trailing "_total" is stripped
// before the unit suffix is appended (and re-added by the exposition), and the
// unit is appended as its own suffix unless the name already carries it.
const (
	OperationDurationName = "wenyuan_operation_duration_seconds"
	OperationTotalName    = "wenyuan_operation_operations_total"

	LLMDurationName       = "wenyuan_llm_duration_seconds"
	LLMCallsName          = "wenyuan_llm_calls_total"
	LLMTokensInputName    = "wenyuan_llm_tokens_input_tokens_total"
	LLMTokensOutputName   = "wenyuan_llm_tokens_output_tokens_total"
	LLMTokensCachedName   = "wenyuan_llm_tokens_cached_input_tokens_total"
	LLMTokensThoughtsName = "wenyuan_llm_tokens_thoughts_tokens_total"

	RetainDocumentsName = "wenyuan_retain_documents_total"

	DBAcquireWaitName = "wenyuan_db_pool_acquire_wait_seconds"

	RecallPhaseDurationName = "wenyuan_recall_phase_duration_seconds"

	RetainPhaseDurationName = "wenyuan_retain_phase_duration_seconds"
	RetainPhaseCallsName    = "wenyuan_retain_phase_calls_total"

	ValidatorPhaseDurationName = "wenyuan_validator_phase_duration_seconds"

	ConsolidationBatchFailuresName = "wenyuan_consolidation_batch_failures_calls_total"

	EventLoopStallsName        = "wenyuan_event_loop_stalls_total"
	EventLoopStallDurationName = "wenyuan_event_loop_stall_duration_seconds"
)

// The HELP text for each family, copied verbatim from the reference's
// instrument descriptions.
const (
	helpOperationDuration = "Duration of operations in seconds"
	helpOperationTotal    = "Total number of operations executed"

	helpLLMDuration       = "Duration of LLM API calls in seconds"
	helpLLMCalls          = "Total number of LLM API calls"
	helpLLMTokensInput    = "Number of input tokens for LLM calls"
	helpLLMTokensOutput   = "Number of output tokens from LLM calls"
	helpLLMTokensCached   = "Number of cached input tokens (billed at cached rate) for LLM calls"
	helpLLMTokensThoughts = "Number of reasoning/thinking tokens emitted by the model (billed as output but not surfaced in candidates)"

	helpRetainDocuments = "Documents processed by retain, labelled by extraction outcome (facts/no_facts)"

	helpDBAcquireWait = "Time spent waiting to acquire a pooled database connection"

	helpRecallPhaseDuration = "Time attributed to one phase of a recall (diagnostic phases are subsets, not siblings)"

	helpRetainPhaseDuration = "Time attributed to one phase of a retain (phases overlap under concurrency)"
	helpRetainPhaseCalls    = "Number of times a retain phase ran -- the round-trip count per phase"

	helpValidatorPhaseDuration = "Time in an operation-validator hook, which runs outside the operation's own timer"

	helpConsolidationBatchFailures = "Consolidation LLM batch calls that failed, by failure class -- including those whose facts adaptive bisection later rescued"

	helpEventLoopStalls        = "Number of detected event-loop stalls (loop blocked past the watchdog threshold)"
	helpEventLoopStallDuration = "Duration of detected event-loop stalls in seconds"
)

// The reference's histogram boundaries, in seconds.
var (
	// DurationBuckets are the boundaries of the operation-duration histogram:
	// fine granularity across the range most operations complete in.
	DurationBuckets = []float64{0.1, 0.25, 0.5, 0.75, 1.0, 2.0, 3.0, 5.0, 7.5, 10.0, 15.0, 20.0, 30.0, 60.0, 120.0}

	// LLMDurationBuckets are the boundaries of the LLM-duration histogram,
	// finer at the low end because a fast call is a fraction of a second.
	LLMDurationBuckets = []float64{0.1, 0.25, 0.5, 1.0, 2.0, 3.0, 5.0, 10.0, 15.0, 30.0, 60.0, 120.0}

	// RecallPhaseBuckets are the boundaries of the recall-phase histogram,
	// sized for phases measured in milliseconds rather than seconds.
	RecallPhaseBuckets = []float64{0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1.0, 2.5, 5.0, 10.0}

	// SDKDefaultBuckets are the boundaries the reference's exporter uses for a
	// histogram with no explicit ones: the OpenTelemetry SDK's own default
	// bounds. The reference declares these families (acquire wait, retain and
	// validator phases, stall duration) without explicit boundaries, so their
	// exposition carries this set.
	SDKDefaultBuckets = []float64{0.0, 5.0, 10.0, 25.0, 50.0, 75.0, 100.0, 250.0, 500.0, 750.0, 1000.0, 2500.0, 5000.0, 7500.0, 10000.0}
)

// WorkloadRegistry accumulates the per-workload families the reference records
// as it does work, in process memory, so a scrape reports exactly the series
// the process has produced. A family with no observation is absent rather than
// rendered as a zeroed series, which is how the reference's registry behaves.
//
// It is safe for concurrent use: the worker, the provider boundary and the
// recall phase observer record from different goroutines while a scrape reads.
type WorkloadRegistry struct {
	mu       sync.Mutex
	counters map[string]map[string]*counterAccum
	hists    map[string]map[string]*histAccum
}

// counterAccum is one labelled counter series.
type counterAccum struct {
	help   string
	labels map[string]string
	value  float64
}

// histAccum is one labelled histogram series.
type histAccum struct {
	help    string
	buckets []float64
	counts  []uint64
	sum     float64
	count   uint64
	labels  map[string]string
}

// Workload is the process-wide accumulator every recording hook writes and the
// exposition reads. Like LoopLag it is package-level so the composition root
// and the recording sites agree on one registry without threading it through
// every call path.
var Workload = NewWorkloadRegistry()

// NewWorkloadRegistry returns an empty registry.
func NewWorkloadRegistry() *WorkloadRegistry {
	return &WorkloadRegistry{
		counters: make(map[string]map[string]*counterAccum),
		hists:    make(map[string]map[string]*histAccum),
	}
}

// labelSignature renders a label set to a stable key: labels sorted by name, so
// two series with the same labels but built in a different order share one
// accumulator.
func labelSignature(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	names := make([]string, 0, len(labels))
	for name := range labels {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		b.WriteString(name)
		b.WriteByte(0x1f)
		b.WriteString(labels[name])
		b.WriteByte(0x1e)
	}
	return b.String()
}

// add records one counter increment on the series identified by name+labels.
func (r *WorkloadRegistry) add(name, help string, labels map[string]string, value float64) {
	key := labelSignature(labels)
	r.mu.Lock()
	defer r.mu.Unlock()
	series, ok := r.counters[name]
	if !ok {
		series = make(map[string]*counterAccum)
		r.counters[name] = series
	}
	accum, ok := series[key]
	if !ok {
		accum = &counterAccum{help: help, labels: cloneLabels(labels)}
		series[key] = accum
	}
	accum.value += value
}

// observe records one histogram observation on the series identified by
// name+labels, creating the buckets on first use.
func (r *WorkloadRegistry) observe(name, help string, buckets []float64, labels map[string]string, value float64) {
	key := labelSignature(labels)
	r.mu.Lock()
	defer r.mu.Unlock()
	series, ok := r.hists[name]
	if !ok {
		series = make(map[string]*histAccum)
		r.hists[name] = series
	}
	accum, ok := series[key]
	if !ok {
		accum = &histAccum{
			help:    help,
			buckets: buckets,
			counts:  make([]uint64, len(buckets)),
			labels:  cloneLabels(labels),
		}
		series[key] = accum
	}
	accum.count++
	accum.sum += value
	for i, bound := range accum.buckets {
		if value <= bound {
			accum.counts[i]++
		}
	}
}

// ObserveOperation records one completed operation's duration and count, under
// the reference's operation, source, budget, max_tokens and success labels.
// budget and max_tokens are always present, empty when the operation carries
// none, as the reference's own exposition shows.
func (r *WorkloadRegistry) ObserveOperation(operation, source, budget, maxTokens string, success bool, seconds float64) {
	labels := map[string]string{
		"operation":  operation,
		"source":     source,
		"budget":     budget,
		"max_tokens": maxTokens,
		"success":    strconv.FormatBool(success),
	}
	r.observe(OperationDurationName, helpOperationDuration, DurationBuckets, labels, seconds)
	r.add(OperationTotalName, helpOperationTotal, labels, 1)
}

// ObserveLLMCall records one completion at the provider boundary: its duration
// and call count, plus each token class the provider reported. A token class
// the provider did not report is not recorded, matching the reference's
// "record only when positive" rule, and each token counter carries the
// reference's token_bucket label.
func (r *WorkloadRegistry) ObserveLLMCall(provider, model, scope string, success bool, seconds float64, input, output, cachedInput, thoughts int) {
	base := map[string]string{
		"provider": provider,
		"model":    model,
		"scope":    scope,
		"success":  strconv.FormatBool(success),
	}
	r.observe(LLMDurationName, helpLLMDuration, LLMDurationBuckets, base, seconds)
	r.add(LLMCallsName, helpLLMCalls, base, 1)
	if input > 0 {
		r.add(LLMTokensInputName, helpLLMTokensInput, withTokenBucket(base, input), float64(input))
	}
	if output > 0 {
		r.add(LLMTokensOutputName, helpLLMTokensOutput, withTokenBucket(base, output), float64(output))
	}
	if cachedInput > 0 {
		r.add(LLMTokensCachedName, helpLLMTokensCached, withTokenBucket(base, cachedInput), float64(cachedInput))
	}
	if thoughts > 0 {
		r.add(LLMTokensThoughtsName, helpLLMTokensThoughts, withTokenBucket(base, thoughts), float64(thoughts))
	}
}

// withTokenBucket returns base with the reference's token_bucket label, which
// keeps a token counter's cardinality bounded while preserving the size class of
// each call. It never mutates the input.
func withTokenBucket(base map[string]string, tokens int) map[string]string {
	out := make(map[string]string, len(base)+1)
	for name, value := range base {
		out[name] = value
	}
	out["token_bucket"] = TokenBucket(tokens)
	return out
}

// TokenBucket maps a token count to the reference's bucket label.
func TokenBucket(tokens int) string {
	switch {
	case tokens < 100:
		return "0-100"
	case tokens < 500:
		return "100-500"
	case tokens < 1000:
		return "500-1k"
	case tokens < 5000:
		return "1k-5k"
	case tokens < 10000:
		return "5k-10k"
	case tokens < 50000:
		return "10k-50k"
	default:
		return "50k+"
	}
}

// CountRetainDocument records one document's retain outcome: "facts" when the
// extraction produced at least one memory unit, "no_facts" when it produced
// none and the document is stored but unreachable through recall.
func (r *WorkloadRegistry) CountRetainDocument(memoryUnitCount int) {
	outcome := "no_facts"
	if memoryUnitCount > 0 {
		outcome = "facts"
	}
	r.add(RetainDocumentsName, helpRetainDocuments, map[string]string{"outcome": outcome}, 1)
}

// ObserveDBAcquireWait records one wait for a pooled database connection.
func (r *WorkloadRegistry) ObserveDBAcquireWait(seconds float64) {
	r.observe(DBAcquireWaitName, helpDBAcquireWait, SDKDefaultBuckets, nil, seconds)
}

// ObserveRecallPhase records one phase of a recall. diagnostic marks a phase
// that is a subset of another rather than a sibling of it, so a consumer
// summing phases can exclude the subsets instead of double-counting.
func (r *WorkloadRegistry) ObserveRecallPhase(phase string, diagnostic bool, seconds float64) {
	r.observe(RecallPhaseDurationName, helpRecallPhaseDuration, RecallPhaseBuckets,
		map[string]string{"phase": phase, "diagnostic": strconv.FormatBool(diagnostic)}, seconds)
}

// ObserveRetainPhase records one phase of a retain: how long it took and how
// many round trips it made, under the reference's phase and store labels. A
// phase the deployment did not reach records nothing, so a family with no
// sample stays absent.
func (r *WorkloadRegistry) ObserveRetainPhase(phase, store string, seconds float64, calls int) {
	labels := map[string]string{"phase": phase}
	if store != "" {
		labels["store"] = store
	}
	r.observe(RetainPhaseDurationName, helpRetainPhaseDuration, SDKDefaultBuckets, labels, seconds)
	r.add(RetainPhaseCallsName, helpRetainPhaseCalls, labels, float64(calls))
}

// ObserveValidatorPhase records one operation-validator hook. The operation
// names the operation the hook gates and the hook is "pre", "post" or "other",
// so a pre-check and a post-charge are separable series.
func (r *WorkloadRegistry) ObserveValidatorPhase(operation, hook string, seconds float64) {
	r.observe(ValidatorPhaseDurationName, helpValidatorPhaseDuration, SDKDefaultBuckets,
		map[string]string{"operation": operation, "hook": hook}, seconds)
}

// CountConsolidationBatchFailure records one failed consolidation LLM batch
// call, labelled by failure class and the exception's type name.
func (r *WorkloadRegistry) CountConsolidationBatchFailure(failureClass, errorType string) {
	r.add(ConsolidationBatchFailuresName, helpConsolidationBatchFailures,
		map[string]string{"failure_class": failureClass, "error_type": errorType}, 1)
}

// ObserveLoopStall records one detected event-loop stall: the stall count and
// the stall's duration.
func (r *WorkloadRegistry) ObserveLoopStall(seconds float64) {
	r.add(EventLoopStallsName, helpEventLoopStalls, nil, 1)
	r.observe(EventLoopStallDurationName, helpEventLoopStallDuration, SDKDefaultBuckets, nil, seconds)
}

// declareLoopStall creates the stall instruments' zero-valued series without
// recording a stall. It is what makes the families visible on a loop that never
// stalled: the reference declares the stall counter and its duration histogram
// together when it builds its collector (metrics.py:623-632), before any stall
// happens, so its exposition carries `wenyuan_event_loop_stalls_total 0.0` and
// the histogram's all-zero bucket/count/sum series. Without the declaration the
// families are absent from a healthy scrape altogether — a missing series rather
// than a zero one, so a rule reading them sees no data where the reference
// reports none.
func (r *WorkloadRegistry) declareLoopStall() {
	r.add(EventLoopStallsName, helpEventLoopStalls, nil, 0)
	r.observe(EventLoopStallDurationName, helpEventLoopStallDuration, SDKDefaultBuckets, nil, 0)
}

// DeclareEventLoopStall publishes the stall counter's zero-valued series on the
// process-wide registry. The process calls it once the event-loop watchdog
// starts, which is when the reference declares its stall instruments.
func DeclareEventLoopStall() { Workload.declareLoopStall() }

// Samples renders every counter (and, as gauges, nothing) as exposition
// samples, sorted by name then label set so a scrape is stable. A family with
// no series contributes nothing.
func (r *WorkloadRegistry) Samples() []Metric {
	r.mu.Lock()
	out := make([]Metric, 0, len(r.counters))
	for name, series := range r.counters {
		for _, accum := range series {
			out = append(out, Metric{
				Name: name, Help: accum.help, Type: "counter",
				Value: accum.value, Labels: cloneLabels(accum.labels),
			})
		}
	}
	r.mu.Unlock()
	if len(out) == 0 {
		return nil
	}
	return sortMetrics(out)
}

// Histograms renders every accumulated histogram family, sorted by name and
// label set. A family with no observation contributes nothing.
func (r *WorkloadRegistry) Histograms() []Histogram {
	r.mu.Lock()
	out := make([]Histogram, 0, len(r.hists))
	for name, series := range r.hists {
		for _, accum := range series {
			counts := make([]uint64, len(accum.counts))
			copy(counts, accum.counts)
			out = append(out, Histogram{
				Name: name, Help: accum.help,
				Buckets: accum.buckets, Counts: counts,
				Sum: accum.sum, Count: accum.count,
				Labels: cloneLabels(accum.labels),
			})
		}
	}
	r.mu.Unlock()
	if len(out) == 0 {
		return nil
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return labelSignature(out[i].Labels) < labelSignature(out[j].Labels)
	})
	return out
}

// cloneLabels copies a label set so a caller cannot mutate a stored series'
// labels after recording.
func cloneLabels(labels map[string]string) map[string]string {
	if len(labels) == 0 {
		return nil
	}
	out := make(map[string]string, len(labels))
	for name, value := range labels {
		out[name] = value
	}
	return out
}
