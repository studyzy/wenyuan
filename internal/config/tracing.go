package config

import "strings"

// TracingConfig is the resolved OpenTelemetry exporter configuration.
//
// Tracing is off by default: a deployment opts in by setting both the enabled
// flag and an endpoint, because an enabled tracer with nowhere to send spans
// is a silent misconfiguration.
type TracingConfig struct {
	// Enabled turns span emission on.
	Enabled bool
	// Endpoint is the OTLP/HTTP collector endpoint.
	Endpoint string
	// Headers are sent on every export request, e.g. an authorization token
	// for a managed backend.
	Headers map[string]string
	// ServiceName and Environment become resource attributes.
	ServiceName string
	Environment string
}

// DefaultTracingServiceName is the service name reported when none is set.
const DefaultTracingServiceName = "hindsight-api"

// DefaultTracingEnvironment is the deployment environment reported by default.
const DefaultTracingEnvironment = "development"

// resolveTracing reads the tracing settings from the environment.
func resolveTracing(lookup func(string) (string, bool)) TracingConfig {
	cfg := TracingConfig{
		ServiceName: DefaultTracingServiceName,
		Environment: DefaultTracingEnvironment,
		Headers:     map[string]string{},
	}
	cfg.Enabled = boolFromEnv(lookup, EnvOTELTracesEnabled, false)
	if value, ok := lookup(EnvOTELExporterOTLPEndpoint); ok {
		cfg.Endpoint = strings.TrimSpace(value)
	}
	if value, ok := lookup(EnvOTELExporterOTLPHeaders); ok {
		cfg.Headers = parseOTLPHeaders(value)
	}
	if value, ok := lookup(EnvOTELServiceName); ok && strings.TrimSpace(value) != "" {
		cfg.ServiceName = strings.TrimSpace(value)
	}
	if value, ok := lookup(EnvOTELDeploymentEnvironment); ok && strings.TrimSpace(value) != "" {
		cfg.Environment = strings.TrimSpace(value)
	}
	// An enabled tracer with no endpoint has nowhere to send spans, so it is
	// treated as disabled rather than silently dropping every span.
	if cfg.Endpoint == "" {
		cfg.Enabled = false
	}
	return cfg
}

// parseOTLPHeaders reads the comma-separated key=value header list the OTLP
// exporter convention uses.
func parseOTLPHeaders(raw string) map[string]string {
	headers := map[string]string{}
	for _, pair := range strings.Split(raw, ",") {
		key, value, found := strings.Cut(pair, "=")
		key = strings.TrimSpace(key)
		if !found || key == "" {
			continue
		}
		headers[key] = strings.TrimSpace(value)
	}
	return headers
}
