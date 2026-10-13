package config

import (
	"strconv"
	"strings"
	"time"
)

// Worker defaults. The attempt budget matches the reference consumer, which
// retries a failed operation three times beyond its first try.
const (
	DefaultWorkerMaxRetries      = 3
	DefaultWorkerConcurrency     = 4
	DefaultWorkerLeaseSeconds    = 300
	DefaultWorkerPollSeconds     = 1
	DefaultWorkerInitialBackoffS = 1
	DefaultWorkerMaxBackoffS     = 60
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
		PollInterval:   DefaultWorkerPollSeconds * time.Second,
		InitialBackoff: DefaultWorkerInitialBackoffS * time.Second,
		MaxBackoff:     DefaultWorkerMaxBackoffS * time.Second,
	}
	cfg.MaxRetries = intFromEnv(lookup, EnvWorkerMaxRetries, cfg.MaxRetries)
	cfg.Concurrency = intFromEnv(lookup, EnvWorkerConcurrency, cfg.Concurrency)
	cfg.Lease = secondsFromEnv(lookup, EnvWorkerLeaseSeconds, cfg.Lease)
	cfg.PollInterval = secondsFromEnv(lookup, EnvWorkerPollSeconds, cfg.PollInterval)
	cfg.InitialBackoff = secondsFromEnv(lookup, EnvWorkerInitialBackoffSeconds, cfg.InitialBackoff)
	cfg.MaxBackoff = secondsFromEnv(lookup, EnvWorkerMaxBackoffSeconds, cfg.MaxBackoff)

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
