package monitoring

import (
	"strings"
	"testing"
)

// findSample returns the first counter sample with the given name.
func findSample(t *testing.T, samples []Metric, name string) Metric {
	t.Helper()
	for _, sample := range samples {
		if sample.Name == name {
			return sample
		}
	}
	t.Fatalf("counter family %q not found in %v", name, samples)
	return Metric{}
}

// findHistogram returns the first histogram family with the given name.
func findHistogram(t *testing.T, families []Histogram, name string) Histogram {
	t.Helper()
	for _, family := range families {
		if family.Name == name {
			return family
		}
	}
	t.Fatalf("histogram family %q not found in %v", name, families)
	return Histogram{}
}

// assertLabels fails unless the sample's label set is exactly want.
func assertLabels(t *testing.T, got map[string]string, want map[string]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("labels = %v, want %v", got, want)
	}
	for name, value := range want {
		if got[name] != value {
			t.Fatalf("label %s = %q, want %q (labels %v)", name, got[name], value, got)
		}
	}
}

// ts-d8-operation: the operation duration and total families are absent until
// an operation settles, then carry the reference's operation, source, budget,
// max_tokens and success labels.
func TestWorkloadOperationFamily(t *testing.T) {
	registry := NewWorkloadRegistry()
	if registry.Samples() != nil || registry.Histograms() != nil {
		t.Fatal("a fresh registry must emit nothing")
	}

	registry.ObserveOperation("retain", "worker", "", "", true, 1.5)

	wantLabels := map[string]string{
		"operation": "retain", "source": "worker", "budget": "", "max_tokens": "", "success": "true",
	}
	total := findSample(t, registry.Samples(), OperationTotalName)
	if total.Value != 1 || total.Type != "counter" {
		t.Fatalf("operation total = %v type %q", total.Value, total.Type)
	}
	assertLabels(t, total.Labels, wantLabels)

	duration := findHistogram(t, registry.Histograms(), OperationDurationName)
	if duration.Count != 1 || duration.Sum != 1.5 {
		t.Fatalf("duration count/sum = %d/%v", duration.Count, duration.Sum)
	}
	if len(duration.Buckets) != len(DurationBuckets) {
		t.Fatalf("duration buckets = %v, want %v", duration.Buckets, DurationBuckets)
	}
	assertLabels(t, duration.Labels, wantLabels)
}

// ts-d8-llm: the LLM families carry provider, model, scope and success; each
// token counter adds the bounded token_bucket dimension, and a token class the
// provider did not report produces no series.
func TestWorkloadLLMFamily(t *testing.T) {
	registry := NewWorkloadRegistry()
	registry.ObserveLLMCall("openai", "gpt-4o", "retain_extract_facts", true, 2.0, 1200, 300, 800, 0)

	base := map[string]string{
		"provider": "openai", "model": "gpt-4o", "scope": "retain_extract_facts", "success": "true",
	}
	calls := findSample(t, registry.Samples(), LLMCallsName)
	if calls.Value != 1 {
		t.Fatalf("calls = %v", calls.Value)
	}
	assertLabels(t, calls.Labels, base)

	duration := findHistogram(t, registry.Histograms(), LLMDurationName)
	if duration.Count != 1 {
		t.Fatalf("duration count = %d", duration.Count)
	}
	assertLabels(t, duration.Labels, base)

	input := findSample(t, registry.Samples(), LLMTokensInputName)
	if input.Value != 1200 {
		t.Fatalf("input tokens = %v", input.Value)
	}
	assertLabels(t, input.Labels, map[string]string{
		"provider": "openai", "model": "gpt-4o", "scope": "retain_extract_facts", "success": "true", "token_bucket": "1k-5k",
	})

	output := findSample(t, registry.Samples(), LLMTokensOutputName)
	assertLabels(t, output.Labels, map[string]string{
		"provider": "openai", "model": "gpt-4o", "scope": "retain_extract_facts", "success": "true", "token_bucket": "100-500",
	})

	cached := findSample(t, registry.Samples(), LLMTokensCachedName)
	assertLabels(t, cached.Labels, map[string]string{
		"provider": "openai", "model": "gpt-4o", "scope": "retain_extract_facts", "success": "true", "token_bucket": "500-1k",
	})

	for _, sample := range registry.Samples() {
		if sample.Name == LLMTokensThoughtsName {
			t.Fatalf("thoughts family emitted with no reasoning tokens: %v", sample)
		}
	}
}

// ts-d8-token-bucket: the bucket label matches the reference's boundaries.
func TestTokenBucket(t *testing.T) {
	cases := map[int]string{
		0: "0-100", 99: "0-100", 100: "100-500", 499: "100-500", 500: "500-1k",
		999: "500-1k", 1000: "1k-5k", 4999: "1k-5k", 5000: "5k-10k", 9999: "5k-10k",
		10000: "10k-50k", 49999: "10k-50k", 50000: "50k+",
	}
	for tokens, want := range cases {
		if got := TokenBucket(tokens); got != want {
			t.Fatalf("TokenBucket(%d) = %q, want %q", tokens, got, want)
		}
	}
}

// ts-d8-retain-documents: a retain counts one document, labelled by whether its
// extraction produced facts.
func TestWorkloadRetainDocuments(t *testing.T) {
	registry := NewWorkloadRegistry()
	registry.CountRetainDocument(0)
	registry.CountRetainDocument(3)

	var facts, noFacts bool
	for _, sample := range registry.Samples() {
		if sample.Name != RetainDocumentsName {
			continue
		}
		switch sample.Labels["outcome"] {
		case "no_facts":
			noFacts = sample.Value == 1
		case "facts":
			facts = sample.Value == 1
		}
		assertLabels(t, sample.Labels, map[string]string{"outcome": sample.Labels["outcome"]})
	}
	if !facts || !noFacts {
		t.Fatalf("retain document outcomes missing: facts=%v no_facts=%v", facts, noFacts)
	}
}

// ts-d8-pool-acquire: a pooled-connection wait records into the acquire-wait
// histogram with the reference's default bucket set.
func TestWorkloadDBAcquireWait(t *testing.T) {
	registry := NewWorkloadRegistry()
	if registry.Histograms() != nil {
		t.Fatal("fresh registry must emit nothing")
	}
	registry.ObserveDBAcquireWait(0.25)
	acquire := findHistogram(t, registry.Histograms(), DBAcquireWaitName)
	if acquire.Count != 1 {
		t.Fatalf("acquire count = %d", acquire.Count)
	}
	if len(acquire.Buckets) != len(SDKDefaultBuckets) {
		t.Fatalf("acquire buckets = %v, want %v", acquire.Buckets, SDKDefaultBuckets)
	}
	if acquire.Labels != nil {
		t.Fatalf("acquire labels = %v, want none", acquire.Labels)
	}
}

// ts-d8-recall-phase: a recall phase records with its phase name and the
// diagnostic split, and uses the phase-specific bucket set.
func TestWorkloadRecallPhase(t *testing.T) {
	registry := NewWorkloadRegistry()
	registry.ObserveRecallPhase("engine_auth", false, 0.002)
	registry.ObserveRecallPhase("semantic_arm", true, 0.05)

	var siblings, subsets bool
	for _, family := range registry.Histograms() {
		if family.Name != RecallPhaseDurationName {
			continue
		}
		if len(family.Buckets) != len(RecallPhaseBuckets) {
			t.Fatalf("recall phase buckets = %v", family.Buckets)
		}
		switch family.Labels["diagnostic"] {
		case "false":
			siblings = family.Labels["phase"] == "engine_auth"
		case "true":
			subsets = family.Labels["phase"] == "semantic_arm"
		}
		assertLabels(t, family.Labels, map[string]string{
			"phase": family.Labels["phase"], "diagnostic": family.Labels["diagnostic"],
		})
	}
	if !siblings || !subsets {
		t.Fatalf("recall phase series missing: siblings=%v subsets=%v", siblings, subsets)
	}
}

// ts-d8-retain-phase: a retain phase records both its duration (phase-specific
// labels, SDK-default buckets) and its call count, and neither family exists
// until a phase is observed.
func TestWorkloadRetainPhase(t *testing.T) {
	registry := NewWorkloadRegistry()
	if registry.Samples() != nil || registry.Histograms() != nil {
		t.Fatal("a fresh registry must emit nothing")
	}
	registry.ObserveRetainPhase("embed", "PostgresStore", 0.02, 3)

	duration := findHistogram(t, registry.Histograms(), RetainPhaseDurationName)
	if len(duration.Buckets) != len(SDKDefaultBuckets) {
		t.Fatalf("retain phase buckets = %v, want SDK defaults", duration.Buckets)
	}
	assertLabels(t, duration.Labels, map[string]string{"phase": "embed", "store": "PostgresStore"})
	calls := findSample(t, registry.Samples(), RetainPhaseCallsName)
	if calls.Value != 3 || calls.Type != "counter" {
		t.Fatalf("calls = %v (%s), want 3 counter", calls.Value, calls.Type)
	}
	assertLabels(t, calls.Labels, map[string]string{"phase": "embed", "store": "PostgresStore"})
}

// ts-d8-validator-phase: a validator hook records its duration under the
// reference's operation and hook labels.
func TestWorkloadValidatorPhase(t *testing.T) {
	registry := NewWorkloadRegistry()
	registry.ObserveValidatorPhase("recall", "pre", 0.004)
	registry.ObserveValidatorPhase("resolve_tag_scope", "other", 0.001)

	var pre, other bool
	for _, family := range registry.Histograms() {
		if family.Name != ValidatorPhaseDurationName {
			continue
		}
		if len(family.Buckets) != len(SDKDefaultBuckets) {
			t.Fatalf("validator phase buckets = %v, want SDK defaults", family.Buckets)
		}
		switch family.Labels["hook"] {
		case "pre":
			pre = family.Labels["operation"] == "recall"
			assertLabels(t, family.Labels, map[string]string{"operation": "recall", "hook": "pre"})
		case "other":
			other = family.Labels["operation"] == "resolve_tag_scope"
			assertLabels(t, family.Labels, map[string]string{"operation": "resolve_tag_scope", "hook": "other"})
		}
	}
	if !pre || !other {
		t.Fatalf("validator phase series missing: pre=%v other=%v", pre, other)
	}
}

// ts-d8-consolidation-failures: a failed consolidation batch records with the
// failure class and error type.
func TestWorkloadConsolidationBatchFailure(t *testing.T) {
	registry := NewWorkloadRegistry()
	registry.CountConsolidationBatchFailure("fail_fast", "ValidationError")
	failure := findSample(t, registry.Samples(), ConsolidationBatchFailuresName)
	if failure.Value != 1 {
		t.Fatalf("failure = %v", failure.Value)
	}
	assertLabels(t, failure.Labels, map[string]string{"failure_class": "fail_fast", "error_type": "ValidationError"})
}

// ts-d8-loop-stalls: a detected stall increments the stall counter and records
// its duration.
func TestWorkloadLoopStall(t *testing.T) {
	registry := NewWorkloadRegistry()
	if registry.Samples() != nil {
		t.Fatal("fresh registry must emit no counters")
	}
	registry.ObserveLoopStall(1.5)
	stalls := findSample(t, registry.Samples(), EventLoopStallsName)
	if stalls.Value != 1 || stalls.Labels != nil {
		t.Fatalf("stalls = %v labels %v", stalls.Value, stalls.Labels)
	}
	duration := findHistogram(t, registry.Histograms(), EventLoopStallDurationName)
	if duration.Count != 1 || duration.Sum != 1.5 {
		t.Fatalf("stall duration = %d/%v", duration.Count, duration.Sum)
	}
}

// ts-d8-exposition: a recorded family reaches the exposition body with the
// instrumentation-scope labels every hindsight_* series carries.
func TestWorkloadFamiliesInExposition(t *testing.T) {
	Workload.ObserveOperation("recall", "api", "mid", "4096", true, 0.5)
	body := string((&MetricsHandler{}).Exposition())
	want := `hindsight_operation_operations_total{budget="mid",max_tokens="4096",operation="recall",otel_scope_name="hindsight_api.metrics",otel_scope_schema_url="",otel_scope_version="",source="api",success="true"} 1`
	if !strings.Contains(body, want) {
		t.Fatalf("operation total missing from exposition:\n%s", body)
	}
	if !strings.Contains(body, "# HELP hindsight_operation_duration_seconds Duration of Hindsight operations in seconds") {
		t.Fatalf("operation duration family missing:\n%s", body)
	}
}
