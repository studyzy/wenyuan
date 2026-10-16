package config

import (
	"strconv"
	"strings"
	"time"
)

// Worker defaults. The attempt budget matches the reference consumer, which
// retries a failed operation three times beyond its first try. The concurrency
// ceiling, poll interval and retry backoff are the reference's own defaults.
const (
	DefaultWorkerMaxRetries      = 3
	DefaultWorkerConcurrency     = 10
	DefaultWorkerLeaseSeconds    = 300
	DefaultWorkerPollInterval    = 500 * time.Millisecond
	DefaultWorkerInitialBackoffS = 60
	DefaultWorkerMaxBackoffS     = 60
	// DefaultWorkerHTTPPort is the reference's 8889 offset by 10000, matching
	// the API port offset: the worker health/metrics surface must not collide
	// with an upstream worker running beside it.
	DefaultWorkerHTTPPort = 18889
)

// WorkerConfig tunes the background queue consumer.
type WorkerConfig struct {
	// ID identifies this consumer among consumers sharing one queue.
	ID string
	// MaxRetries is the attempt budget beyond the first try.
	MaxRetries int
	// Concurrency is how many operations one consumer processes at once.
	Concurrency int
	// Lease bounds how long a claim stays valid.
	Lease time.Duration
	// PollInterval is how long to wait between empty claims.
	PollInterval time.Duration
	// InitialBackoff is the first retry delay.
	InitialBackoff time.Duration
	// MaxBackoff is the retry backoff ceiling.
	MaxBackoff time.Duration
	// HTTPPort is the port the worker's health/metrics surface binds. Zero
	// disables it.
	HTTPPort int
	// WallTimeouts bounds one operation type's wall-clock budget. A type absent
	// from the map is bounded only by the claim's lease.
	WallTimeouts map[string]time.Duration
	// ReservedSlots reserves a floor of the consumer's concurrency for a type.
	ReservedSlots map[string]int
	// RecallMaxCandidatesPerSource caps each recall retrieval arm before fusion.
	// Zero disables the cap.
	RecallMaxCandidatesPerSource int
}

// resolveWallTimeouts reads the per-operation wall ceilings. A key that is
// absent leaves the operation bounded only by the claim's lease.
//
// The reference spells these HINDSIGHT_API_<OP>_WALL_TIMEOUT; this project
// shipped the same knob earlier as HINDSIGHT_API_WORKER_<OP>_WALL_TIMEOUT_SECONDS.
// The reference name is read first and the earlier name is honoured as a
// fallback, so an environment written either way resolves the same ceiling.
func resolveWallTimeouts(lookup func(string) (string, bool)) map[string]time.Duration {
	// The map is keyed by *operation type*, because that is what the consumer
	// looks up (`WallTimeouts[op.OperationType]`). A refresh is one reflect plus
	// its write, so it shares reflect's budget and the ceiling is keyed by the
	// operation a refresh actually claims - the reference records the same
	// reasoning, and without it a wedged refresh held its worker slot until
	// restart (#4581). Keying it "reflect" meant no claimed operation ever
	// matched, so refreshes ran unbounded.
	out := map[string]time.Duration{
		// The reference's per-operation ceilings, applied when neither the
		// reference key nor the legacy alias is set.
		"retain":               DefaultRetainWallTimeoutS * time.Second,
		"consolidation":        DefaultConsolidationWallTimeoutS * time.Second,
		"refresh_mental_model": DefaultReflectWallTimeoutS * time.Second,
	}
	for _, spec := range []struct{ ref, legacy, opType string }{
		{EnvRetainWallTimeout, EnvWorkerRetainWallSeconds, "retain"},
		{EnvConsolidationWallTimeout, EnvWorkerConsolidationWallSeconds, "consolidation"},
		{EnvReflectWallTimeout, EnvWorkerReflectWallSeconds, "refresh_mental_model"},
	} {
		key := spec.ref
		if _, ok := lookup(key); !ok {
			key = spec.legacy
		}
		if value, ok := lookup(key); ok {
			if seconds := intFromString(value); seconds > 0 {
				out[spec.opType] = time.Duration(seconds) * time.Second
			}
		}
	}
	return out
}

// The reference's default per-operation wall ceilings, in seconds.
const (
	DefaultRetainWallTimeoutS        = 3600
	DefaultConsolidationWallTimeoutS = 7200
	DefaultReflectWallTimeoutS       = 300
)

// resolveReservedSlots reads the per-type concurrency floors. The consolidation
// floor defaults to two, matching the reference, so a bank mid-bulk-ingest does
// not starve observation folding.
func resolveReservedSlots(lookup func(string) (string, bool)) map[string]int {
	out := map[string]int{"consolidation": 2}
	if value, ok := lookup(EnvWorkerConsolidationReservedSlots); ok {
		if slots := intFromString(value); slots >= 0 {
			out["consolidation"] = slots
		}
	}
	// The reference reserves a floor per operation type, under one key per type.
	// A type whose floor is zero reserves nothing, which is the same as leaving
	// the entry out: the consumer only prefers a type whose floor is unmet.
	for key, opType := range reservedSlotKeys {
		if value, ok := lookup(key); ok {
			if slots := intFromString(value); slots > 0 {
				out[opType] = slots
			}
		}
	}
	return out
}

// reservedSlotKeys maps each per-type reservation key to the operation type it
// reserves capacity for. The type names are the storage layer's own operation
// type strings, so the reservation lands on the work the operator meant.
var reservedSlotKeys = map[string]string{
	EnvWorkerRetainReservedSlots:             "retain",
	EnvWorkerFileConvertRetainReservedSlots:  "file_convert_retain",
	EnvWorkerRefreshMentalModelReservedSlots: "refresh_mental_model",
	EnvWorkerGraphMaintenanceReservedSlots:   "graph_maintenance",
	EnvWorkerVectorIndexMaintReservedSlots:   "vector_index_maintenance",
	EnvWorkerImportDocumentsReservedSlots:    "import_documents",
	EnvWorkerExportDocumentsReservedSlots:    "export_documents",
	EnvWorkerImportBankReservedSlots:         "import_bank",
	EnvWorkerExportBankReservedSlots:         "export_bank",
	EnvWorkerCloneBankReservedSlots:          "clone_bank",
}

// resolveWorker reads the worker tuning through the shared resolution rules: a
// blank or unparseable value falls back to the default rather than failing
// startup, matching the rest of the configuration surface.
func resolveWorker(lookup func(string) (string, bool)) WorkerConfig {
	cfg := WorkerConfig{
		ID:             stringFromEnv(lookup, EnvWorkerID),
		MaxRetries:     DefaultWorkerMaxRetries,
		Concurrency:    DefaultWorkerConcurrency,
		Lease:          DefaultWorkerLeaseSeconds * time.Second,
		PollInterval:   DefaultWorkerPollInterval,
		InitialBackoff: DefaultWorkerInitialBackoffS * time.Second,
		MaxBackoff:     DefaultWorkerMaxBackoffS * time.Second,
	}
	cfg.MaxRetries = intFromEnv(lookup, EnvWorkerMaxRetries, cfg.MaxRetries)
	cfg.Concurrency = intFromEnv(lookup, EnvWorkerConcurrency, cfg.Concurrency)
	cfg.Lease = secondsFromEnv(lookup, EnvWorkerLeaseSeconds, cfg.Lease)
	cfg.PollInterval = secondsFromEnv(lookup, EnvWorkerPollSeconds, cfg.PollInterval)
	cfg.WallTimeouts = resolveWallTimeouts(lookup)
	cfg.ReservedSlots = resolveReservedSlots(lookup)
	cfg.RecallMaxCandidatesPerSource = intFromEnv(lookup, EnvRecallMaxCandidatesPerSource, DefaultRecallMaxCandidatesPerSource)
	cfg.InitialBackoff = secondsFromEnv(lookup, EnvWorkerInitialBackoffSeconds, cfg.InitialBackoff)
	cfg.MaxBackoff = secondsFromEnv(lookup, EnvWorkerMaxBackoffSeconds, cfg.MaxBackoff)
	cfg.HTTPPort = intFromEnv(lookup, EnvWorkerHTTPPort, DefaultWorkerHTTPPort)
	// Reference aliases: the reference names these keys differently and
	// expresses the poll interval in milliseconds, so an existing deployment's
	// environment is honoured rather than silently ignored.
	if cfg.Concurrency == DefaultWorkerConcurrency {
		cfg.Concurrency = intFromEnv(lookup, EnvWorkerMaxSlots, cfg.Concurrency)
	}
	if cfg.InitialBackoff == DefaultWorkerInitialBackoffS*time.Second {
		cfg.InitialBackoff = secondsFromEnv(lookup, EnvWorkerTaskRetryBackoffSecs, cfg.InitialBackoff)
	}
	if cfg.PollInterval == DefaultWorkerPollInterval {
		if value, ok := lookup(EnvWorkerPollIntervalMS); ok {
			if ms := intFromString(value); ms > 0 {
				cfg.PollInterval = time.Duration(ms) * time.Millisecond
			}
		}
	}

	// A non-positive attempt budget would mean "no retry at all", so it is
	// clamped at zero rather than allowed to go negative.
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = DefaultWorkerConcurrency
	}
	if cfg.MaxBackoff < cfg.InitialBackoff {
		cfg.MaxBackoff = cfg.InitialBackoff
	}
	return cfg
}

// secondsFromEnv reads a duration expressed in seconds, falling back on a
// blank or unparseable value.
func secondsFromEnv(lookup func(string) (string, bool), key string, fallback time.Duration) time.Duration {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	seconds, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}

// hostListFromEnv reads a comma-separated host allowlist. A blank value yields
// an empty list, which the guard treats as "no allowlist configured".
func hostListFromEnv(lookup func(string) (string, bool), key string) []string {
	return stringListFromEnv(lookup, key)
}

// stringListFromEnv reads a comma-separated list, trimming each element and
// dropping empties. A blank or absent value yields nil, which callers read as
// "unset" distinct from an explicit empty list.
func stringListFromEnv(lookup func(string) (string, bool), key string) []string {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
