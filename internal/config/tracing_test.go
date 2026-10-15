package config

import "testing"

// Tracing is off until a deployment enables it and names an endpoint.
func TestResolveTracing(t *testing.T) {
	cases := []struct {
		name     string
		env      map[string]string
		enabled  bool
		endpoint string
		headers  map[string]string
		service  string
		envName  string
	}{
		{
			name:    "off by default",
			service: DefaultTracingServiceName,
			envName: DefaultTracingEnvironment,
		},
		{
			name:     "enabled with endpoint",
			env:      map[string]string{EnvOTELTracesEnabled: "true", EnvOTELExporterOTLPEndpoint: "http://collector:4318"},
			enabled:  true,
			endpoint: "http://collector:4318",
			service:  DefaultTracingServiceName,
			envName:  DefaultTracingEnvironment,
		},
		{
			name:    "enabled without endpoint stays off",
			env:     map[string]string{EnvOTELTracesEnabled: "true"},
			service: DefaultTracingServiceName,
			envName: DefaultTracingEnvironment,
		},
		{
			name: "headers, service and environment override",
			env: map[string]string{
				EnvOTELTracesEnabled:         "true",
				EnvOTELExporterOTLPEndpoint:  "https://cloud.example/api/public/otel",
				EnvOTELExporterOTLPHeaders:   "Authorization=Bearer abc, x-scope=team",
				EnvOTELServiceName:           "wenyuan-prod",
				EnvOTELDeploymentEnvironment: "production",
			},
			enabled:  true,
			endpoint: "https://cloud.example/api/public/otel",
			headers:  map[string]string{"Authorization": "Bearer abc", "x-scope": "team"},
			service:  "wenyuan-prod",
			envName:  "production",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FromEnv(envMap(tc.env)).Tracing
			if got.Enabled != tc.enabled {
				t.Fatalf("enabled = %v, want %v", got.Enabled, tc.enabled)
			}
			if got.Endpoint != tc.endpoint {
				t.Fatalf("endpoint = %q, want %q", got.Endpoint, tc.endpoint)
			}
			if got.ServiceName != tc.service {
				t.Fatalf("service = %q, want %q", got.ServiceName, tc.service)
			}
			if got.Environment != tc.envName {
				t.Fatalf("environment = %q, want %q", got.Environment, tc.envName)
			}
			for key, want := range tc.headers {
				if got.Headers[key] != want {
					t.Fatalf("header %q = %q, want %q", key, got.Headers[key], want)
				}
			}
		})
	}
}
