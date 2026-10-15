package cli

import (
	"context"
	"fmt"
)

// healthBody mirrors the served /health payload; the JSON contract lives with
// the API, so the client declares its own view instead of importing the
// server's monitoring package.
type healthBody struct {
	Status      string   `json:"status"`
	Database    string   `json:"database,omitempty"`
	Version     string   `json:"version,omitempty"`
	DBAcquireMS *float64 `json:"db_acquire_ms,omitempty"`
	Error       string   `json:"error,omitempty"`
	Reason      string   `json:"reason,omitempty"`
	PoolInUse   *int     `json:"db_pool_in_use,omitempty"`
	PoolMax     *int     `json:"db_pool_max,omitempty"`
	PoolIdle    *int     `json:"db_pool_idle,omitempty"`
}

// versionBody mirrors the served /version payload.
type versionBody struct {
	APIVersion string          `json:"api_version"`
	Features   map[string]bool `json:"features"`
}

// Health implements `wenyuan-admin health`: it decodes the readiness payload
// and renders it exactly as the reference does — a section header, the status
// tinted by the gradient start colour when healthy and the end colour
// otherwise, then the database state (tinted the same way when connected) and
// the version when the server reports one. A failing readiness route is a
// non-2xx response, which the error classifier renders.
func Health(ctx context.Context, d *Deps) error {
	var body healthBody
	if err := d.Client.Do(ctx, "GET", "/health", nil, &body); err != nil {
		return err
	}
	if !d.Format.IsMachine() {
		d.UI.Section("Health Check")
		d.UI.Plain("  %s %s", d.UI.Dim("Status:"), d.UI.statusPaint(body.Status, body.Status == "healthy"))
		if body.Database != "" {
			d.UI.Plain("  %s %s", d.UI.Dim("Database:"), d.UI.statusPaint(body.Database, body.Database == "connected"))
		}
		if body.Version != "" {
			d.UI.Plain("  %s %s", d.UI.Dim("Version:"), body.Version)
		}
		d.UI.Plain("")
		return nil
	}
	return d.Format.Render(d.UI.Stdout, body)
}

// Metrics implements `wenyuan-admin metrics`: the Prometheus surface is text,
// not JSON. Pretty output prints it under a section header; the machine
// formats wrap it in a {"metrics": ...} object, as the reference does.
func Metrics(ctx context.Context, d *Deps) error {
	text, err := d.Client.GetText(ctx, "/metrics")
	if err != nil {
		return err
	}
	if !d.Format.IsMachine() {
		d.UI.Section("Prometheus Metrics")
		_, err = fmt.Fprintln(d.UI.Stdout, text)
		return err
	}
	return d.Format.Render(d.UI.Stdout, map[string]string{"metrics": text})
}

// VersionInfo implements `wenyuan-admin version`.
func VersionInfo(ctx context.Context, d *Deps) error {
	var body versionBody
	if err := d.Client.Do(ctx, "GET", "/version", nil, &body); err != nil {
		return err
	}
	if !d.Format.IsMachine() {
		d.UI.Section("API Version")
		d.UI.Plain("  %s %s", d.UI.Dim("Version:"), body.APIVersion)
		d.UI.Plain("")
		d.UI.Plain("  %s", d.UI.Dim("Features:"))
		for _, f := range versionFeatures {
			d.UI.Plain("    %s %s: %s", d.UI.paint("•", brandStart), f.label, enabledWord(body.Features[f.key]))
		}
		d.UI.Plain("")
		return nil
	}
	return d.Format.Render(d.UI.Stdout, body)
}

// versionFeature names one entry of the version payload's feature map, in the
// order the reference prints them.
type versionFeature struct {
	key   string
	label string
}

// versionFeatures is the ordered feature list the reference renders.
var versionFeatures = []versionFeature{
	{key: "mcp", label: "MCP Server"},
	{key: "observations", label: "Observations"},
	{key: "worker", label: "Background Worker"},
}

// enabledWord renders a feature flag the way the reference does.
func enabledWord(on bool) string {
	if on {
		return "enabled"
	}
	return "disabled"
}

type configureOut struct {
	APIURL     string `json:"api_url" yaml:"api_url"`
	APIKeySet  bool   `json:"api_key_set" yaml:"api_key_set"`
	ConfigPath string `json:"config_path" yaml:"config_path"`
}

// Configure implements `wenyuan-admin configure`. It loads without profile support
// (no -p and no profile environment variable) to display the current state,
// validates the new URL, keeps the stored key unless a new one is given, and
// saves. A missing --api-url prompts on stdin regardless of the output
// format, so machine formats should always pass the flag.
func Configure(d *Deps, apiURLFlag, apiKeyFlag string) error {
	ui, format := d.UI, d.Format
	cfg, err := Load(LoadOptions{IgnoreProfileEnv: true})
	if err != nil {
		return err
	}
	if !format.IsMachine() {
		ui.Infof("Wenyuan CLI Configuration")
		ui.Plain("")
		ui.Plain("  Current API URL: %s", cfg.APIURL)
		if cfg.APIKey != "" {
			ui.Plain("  Current API Key: %s", MaskKey(cfg.APIKey))
		}
		ui.Plain("  Source: %s", cfg.Source)
		ui.Plain("")
	}

	url := apiURLFlag
	if url == "" {
		_, _ = fmt.Fprintf(ui.Stdout, "Enter API URL [%s]: ", cfg.APIURL)
		url = readLine(d.Stdin)
		if url == "" {
			url = cfg.APIURL
		}
	}
	if err := ValidateAPIURL(url); err != nil {
		return err
	}
	key := cfg.APIKey
	if apiKeyFlag != "" {
		key = apiKeyFlag
	}
	home, err := UserHome()
	if err != nil {
		return err
	}
	path, err := SaveConfig(home, url, key)
	if err != nil {
		return err
	}
	if !format.IsMachine() {
		ui.Infof("Configuration saved to %s", path)
		ui.Plain("")
		ui.Plain("  API URL: %s", url)
		if key != "" {
			ui.Plain("  API Key: %s", MaskKey(key))
		}
		ui.Plain("")
		ui.Plain("Note: Environment variables %s and %s will override these settings.", EnvAPIURL, EnvAPIKey)
		return nil
	}
	return format.Render(ui.Stdout, configureOut{APIURL: url, APIKeySet: key != "", ConfigPath: path})
}
