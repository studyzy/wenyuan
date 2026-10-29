package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// tenantView is the management plane's tenant representation.
type tenantView struct {
	TenantID    string         `json:"tenant_id" yaml:"tenant_id"`
	DisplayName string         `json:"display_name" yaml:"display_name"`
	Status      string         `json:"status" yaml:"status"`
	Config      map[string]any `json:"config,omitempty" yaml:"config,omitempty"`
	CreatedAt   string         `json:"created_at,omitempty" yaml:"created_at,omitempty"`
	UpdatedAt   string         `json:"updated_at,omitempty" yaml:"updated_at,omitempty"`
	DeletedAt   *string        `json:"deleted_at,omitempty" yaml:"deleted_at,omitempty"`
}

// tenantKeyView is a credential without its hash or plaintext.
type tenantKeyView struct {
	KeyID      string  `json:"key_id" yaml:"key_id"`
	Prefix     string  `json:"prefix" yaml:"prefix"`
	Label      string  `json:"label" yaml:"label"`
	Ephemeral  bool    `json:"ephemeral" yaml:"ephemeral"`
	ExpiresAt  *string `json:"expires_at,omitempty" yaml:"expires_at,omitempty"`
	RevokedAt  *string `json:"revoked_at,omitempty" yaml:"revoked_at,omitempty"`
	LastUsedAt *string `json:"last_used_at,omitempty" yaml:"last_used_at,omitempty"`
	CreatedAt  string  `json:"created_at,omitempty" yaml:"created_at,omitempty"`
}

// issuedKeyView is the one response that carries a plaintext credential.
type issuedKeyView struct {
	Plaintext string        `json:"plaintext" yaml:"plaintext"`
	Key       tenantKeyView `json:"key" yaml:"key"`
}

// RegisterTenantCommands wires the management-plane tenant family.
func RegisterTenantCommands(r *Registry) {
	tenant := r.Sub("tenant", "manage tenants through the management plane")
	registerTenantCore(tenant)
	registerTenantKeys(tenant)
}

func registerTenantCore(tenant *SubCmd) {
	list := tenant.Sub("list", "list tenants")
	status := list.Flags().String("status", "", "filter by status")
	query := list.Flags().String("q", "", "search id or display name")
	limit := list.Flags().Int("limit", 0, "page size")
	offset := list.Flags().Int("offset", 0, "page offset")
	list.Action(func(ctx context.Context, d *Deps) error {
		values := url.Values{}
		if *status != "" {
			values.Set("status", *status)
		}
		if *query != "" {
			values.Set("q", *query)
		}
		if *limit > 0 {
			values.Set("limit", fmt.Sprint(*limit))
		}
		if *offset > 0 {
			values.Set("offset", fmt.Sprint(*offset))
		}
		path := "/admin/v1/tenants"
		if encoded := values.Encode(); encoded != "" {
			path += "?" + encoded
		}
		var response struct {
			Tenants []tenantView `json:"tenants"`
		}
		if err := d.Client.Do(ctx, "GET", path, nil, &response); err != nil {
			return err
		}
		if d.Format.IsMachine() {
			return d.Format.Render(d.UI.Stdout, response)
		}
		if len(response.Tenants) == 0 {
			d.UI.Infof("No tenants found.")
			return nil
		}
		for _, entry := range response.Tenants {
			d.UI.Plain("%s	%s	%s", entry.TenantID, entry.Status, entry.DisplayName)
		}
		return nil
	})

	create := tenant.Sub("create", "create a tenant")
	displayName := create.Flags().String("display-name", "", "display name")
	config := create.Flags().String("config", "", "config as a JSON object")
	adopt := create.Flags().Bool("adopt", false, "adopt an existing schema")
	create.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("tenant create", d.Args, 1); err != nil {
			return err
		}
		parsed, err := parseTenantConfig(*config)
		if err != nil {
			return err
		}
		body := map[string]any{"tenant_id": d.Args[0], "adopt": *adopt}
		if *displayName != "" {
			body["display_name"] = *displayName
		}
		if parsed != nil {
			body["config"] = parsed
		}
		var response tenantView
		if err := d.Client.Do(ctx, "POST", "/admin/v1/tenants", body, &response); err != nil {
			return err
		}
		return renderTenant(d, response)
	})

	get := tenant.Sub("get", "show one tenant")
	get.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("tenant get", d.Args, 1); err != nil {
			return err
		}
		var response tenantView
		if err := d.Client.Do(ctx, "GET", tenantPath(d.Args[0]), nil, &response); err != nil {
			return err
		}
		return renderTenant(d, response)
	})

	del := tenant.Sub("delete", "soft-delete a tenant")
	del.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("tenant delete", d.Args, 1); err != nil {
			return err
		}
		var response tenantView
		if err := d.Client.Do(ctx, "DELETE", tenantPath(d.Args[0]), nil, &response); err != nil {
			return err
		}
		return renderTenant(d, response)
	})

	overview := tenant.Sub("overview", "count tenants by status")
	overview.Action(func(ctx context.Context, d *Deps) error {
		var counts map[string]int
		if err := d.Client.Do(ctx, "GET", "/admin/v1/overview", nil, &counts); err != nil {
			return err
		}
		if d.Format.IsMachine() {
			return d.Format.Render(d.UI.Stdout, counts)
		}
		d.UI.Plain("total %d", counts["total"])
		for status, count := range counts {
			if status != "total" && count > 0 {
				d.UI.Plain("%s %d", status, count)
			}
		}
		return nil
	})

	migrate := tenant.Sub("migrate", "migrate one tenant schema")
	migrate.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("tenant migrate", d.Args, 1); err != nil {
			return err
		}
		var response map[string]string
		if err := d.Client.Do(ctx, "POST", tenantPath(d.Args[0])+"/migrate", nil, &response); err != nil {
			return err
		}
		if d.Format.IsMachine() {
			return d.Format.Render(d.UI.Stdout, response)
		}
		d.UI.Plain("%s", response["status"])
		return nil
	})
}

func registerTenantKeys(tenant *SubCmd) {
	key := tenant.Sub("key", "manage tenant credentials")

	list := key.Sub("list", "list a tenant's keys")
	list.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("tenant key list", d.Args, 1); err != nil {
			return err
		}
		var response struct {
			Keys []tenantKeyView `json:"keys"`
		}
		if err := d.Client.Do(ctx, "GET", tenantPath(d.Args[0])+"/keys", nil, &response); err != nil {
			return err
		}
		if d.Format.IsMachine() {
			return d.Format.Render(d.UI.Stdout, response)
		}
		for _, entry := range response.Keys {
			d.UI.Plain("%s	%s	%s", entry.KeyID, entry.Prefix, entry.Label)
		}
		return nil
	})

	create := key.Sub("create", "create a tenant key")
	label := create.Flags().String("label", "", "label for the key")
	expiresAt := create.Flags().String("expires-at", "", "RFC3339 expiry")
	ephemeral := create.Flags().Bool("ephemeral", false, "short-lived key")
	create.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("tenant key create", d.Args, 1); err != nil {
			return err
		}
		body := map[string]any{"ephemeral": *ephemeral}
		if *label != "" {
			body["label"] = *label
		}
		if *expiresAt != "" {
			body["expires_at"] = *expiresAt
		}
		var response issuedKeyView
		if err := d.Client.Do(ctx, "POST", tenantPath(d.Args[0])+"/keys", body, &response); err != nil {
			return err
		}
		return renderIssuedKey(d, response)
	})

	revoke := key.Sub("revoke", "revoke a tenant key")
	revoke.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("tenant key revoke", d.Args, 2); err != nil {
			return err
		}
		var response map[string]string
		path := tenantPath(d.Args[0]) + "/keys/" + url.PathEscape(d.Args[1])
		if err := d.Client.Do(ctx, "DELETE", path, nil, &response); err != nil {
			return err
		}
		if d.Format.IsMachine() {
			return d.Format.Render(d.UI.Stdout, response)
		}
		d.UI.Plain("%s", response["status"])
		return nil
	})

	session := tenant.Sub("session", "mint a short-lived tenant key")
	session.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("tenant session", d.Args, 1); err != nil {
			return err
		}
		var response issuedKeyView
		if err := d.Client.Do(ctx, "POST", tenantPath(d.Args[0])+"/session", nil, &response); err != nil {
			return err
		}
		return renderIssuedKey(d, response)
	})
}

func tenantPath(id string) string {
	return "/admin/v1/tenants/" + url.PathEscape(id)
}

func renderTenant(d *Deps, tenant tenantView) error {
	if d.Format.IsMachine() {
		return d.Format.Render(d.UI.Stdout, tenant)
	}
	d.UI.Plain("%s	%s	%s", tenant.TenantID, tenant.Status, tenant.DisplayName)
	return nil
}

func renderIssuedKey(d *Deps, issued issuedKeyView) error {
	if d.Format.IsMachine() {
		return d.Format.Render(d.UI.Stdout, issued)
	}
	d.UI.Infof("Key created (shown once):")
	d.UI.Plain("%s", issued.Plaintext)
	return nil
}

func parseTenantConfig(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, NewCommandError("--config must be a JSON object")
	}
	return parsed, nil
}
