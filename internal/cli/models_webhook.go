package cli

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// webhookHTTPCfg is the typed HTTP delivery configuration.
type webhookHTTPCfg struct {
	Method         string            `json:"method" yaml:"method"`
	TimeoutSeconds int               `json:"timeout_seconds" yaml:"timeout_seconds"`
	Headers        map[string]string `json:"headers" yaml:"headers"`
	Params         map[string]string `json:"params" yaml:"params"`
}

// webhookOut is the wire shape of a webhook. The secret is always present
// and null: it is never readable back.
type webhookOut struct {
	ID         string         `json:"id" yaml:"id"`
	BankID     string         `json:"bank_id" yaml:"bank_id"`
	URL        string         `json:"url" yaml:"url"`
	Secret     *string        `json:"secret" yaml:"secret"`
	EventTypes []string       `json:"event_types" yaml:"event_types"`
	Enabled    bool           `json:"enabled" yaml:"enabled"`
	HTTPConfig webhookHTTPCfg `json:"http_config" yaml:"http_config"`
	CreatedAt  string         `json:"created_at" yaml:"created_at"`
	UpdatedAt  string         `json:"updated_at" yaml:"updated_at"`
}

// webhookListOut is the webhook listing envelope.
type webhookListOut struct {
	Items  []webhookOut `json:"items" yaml:"items"`
	Total  int          `json:"total" yaml:"total"`
	Limit  int          `json:"limit" yaml:"limit"`
	Offset int          `json:"offset" yaml:"offset"`
}

// webhookCreateBody is the create request body. http_config stays null: the
// server fills the POST/30s defaults.
type webhookCreateBody struct {
	URL        string          `json:"url"`
	Secret     *string         `json:"secret,omitempty"`
	EventTypes []string        `json:"event_types"`
	Enabled    bool            `json:"enabled"`
	HTTPConfig *webhookHTTPCfg `json:"http_config"`
}

// webhookUpdateBody is the partial-update body; absent fields are omitted.
type webhookUpdateBody struct {
	URL        *string   `json:"url,omitempty"`
	EventTypes *[]string `json:"event_types,omitempty"`
	Enabled    *bool     `json:"enabled,omitempty"`
	Secret     *string   `json:"secret,omitempty"`
}

// webhookDeleteOut is the delete acknowledgement.
type webhookDeleteOut struct {
	Success      bool   `json:"success" yaml:"success"`
	Message      string `json:"message,omitempty" yaml:"message,omitempty"`
	DeletedCount *int   `json:"deleted_count,omitempty" yaml:"deleted_count,omitempty"`
}

// webhookDeliveryOut is one recorded delivery attempt.
type webhookDeliveryOut struct {
	ID                 string  `json:"id" yaml:"id"`
	WebhookID          string  `json:"webhook_id" yaml:"webhook_id"`
	URL                string  `json:"url" yaml:"url"`
	EventType          string  `json:"event_type" yaml:"event_type"`
	Status             string  `json:"status" yaml:"status"`
	Attempts           int     `json:"attempts" yaml:"attempts"`
	NextRetryAt        *string `json:"next_retry_at" yaml:"next_retry_at"`
	LastError          *string `json:"last_error" yaml:"last_error"`
	LastResponseStatus *int    `json:"last_response_status" yaml:"last_response_status"`
	LastResponseBody   *string `json:"last_response_body" yaml:"last_response_body"`
	LastAttemptAt      *string `json:"last_attempt_at" yaml:"last_attempt_at"`
	CreatedAt          *string `json:"created_at" yaml:"created_at"`
	UpdatedAt          *string `json:"updated_at" yaml:"updated_at"`
}

// webhookDeliveriesOut is the delivery listing envelope.
type webhookDeliveriesOut struct {
	Items      []webhookDeliveryOut `json:"items" yaml:"items"`
	NextCursor *string              `json:"next_cursor" yaml:"next_cursor"`
}

// RegisterWebhookFamily wires the webhook commands onto the registry. The
// server has no single-webhook GET, so there is no `get` subcommand.
func RegisterWebhookFamily(r *Registry) {
	wh := r.Sub("webhook", "manage event delivery webhooks")

	list := wh.Sub("list", "list webhooks of a bank")
	list.Action(func(ctx context.Context, d *Deps) error {
		if len(d.Args) != 1 {
			return NewUsageError("webhook list requires exactly one <bank_id>")
		}
		return webhookList(ctx, d, d.Args[0])
	})

	create := wh.Sub("create", "create a webhook")
	var cf webhookCreateFlags
	create.Flags().StringVar(&cf.eventTypes, "event-types", "", "comma-separated event types")
	create.Flags().BoolVar(&cf.disabled, "disabled", false, "create the webhook disabled")
	create.Flags().StringVar(&cf.secret, "secret", "", "HMAC-SHA256 signing secret")
	create.Action(func(ctx context.Context, d *Deps) error {
		if len(d.Args) != 2 {
			return NewUsageError("webhook create requires <bank_id> and <url>")
		}
		return webhookCreate(ctx, d, d.Args[0], d.Args[1], cf)
	})

	update := wh.Sub("update", "update a webhook")
	var uf webhookUpdateFlags
	update.Flags().StringVar(&uf.url, "url", "", "new URL")
	update.Flags().StringVar(&uf.eventTypes, "event-types", "", "comma-separated event types (replaces the set)")
	update.Flags().Var(&uf.enabled, "enabled", "enabled state (true|false)")
	update.Flags().StringVar(&uf.secret, "secret", "", "new signing secret (replaces)")
	update.Action(func(ctx context.Context, d *Deps) error {
		if len(d.Args) != 2 {
			return NewUsageError("webhook update requires <bank_id> and <webhook_id>")
		}
		return webhookUpdate(ctx, d, d.Args[0], d.Args[1], &uf)
	})

	del := wh.Sub("delete", "delete a webhook")
	delYes := del.Flags().Bool("yes", false, "skip the confirmation prompt")
	del.Flags().BoolVar(delYes, "y", false, "skip the confirmation prompt (short)")
	del.Action(func(ctx context.Context, d *Deps) error {
		if len(d.Args) != 2 {
			return NewUsageError("webhook delete requires <bank_id> and <webhook_id>")
		}
		return webhookDelete(ctx, d, d.Args[0], d.Args[1], *delYes)
	})

	deliveries := wh.Sub("deliveries", "show delivery attempts of a webhook")
	deliveriesCursor := deliveries.Flags().String("cursor", "", "page cursor from the previous call")
	var deliveriesLimit triStateInt
	deliveries.Flags().Var(&deliveriesLimit, "limit", "page size")
	deliveries.Flags().Var(&deliveriesLimit, "l", "page size (short)")
	deliveries.Action(func(ctx context.Context, d *Deps) error {
		if len(d.Args) != 2 {
			return NewUsageError("webhook deliveries requires <bank_id> and <webhook_id>")
		}
		return webhookDeliveries(ctx, d, d.Args[0], d.Args[1], *deliveriesCursor, deliveriesLimit)
	})
}

// webhookCreateFlags holds the create flag values.
type webhookCreateFlags struct {
	eventTypes string
	disabled   bool
	secret     string
}

// webhookUpdateFlags holds the update flag values.
type webhookUpdateFlags struct {
	url        string
	eventTypes string
	enabled    triStateBool
	secret     string
}

func (f *webhookUpdateFlags) any() bool {
	return f.url != "" || f.eventTypes != "" || f.enabled.set || f.secret != ""
}

func webhookList(ctx context.Context, d *Deps, bank string) error {
	var out webhookListOut
	path := fmt.Sprintf("%s/%s/webhooks", batchDBase, bank)
	if err := d.Client.Do(ctx, "GET", path, nil, &out); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return d.Format.Render(d.UI.Stdout, out)
	}
	ui := d.UI
	ui.Infof("Webhooks: %s", bank)
	if len(out.Items) == 0 {
		ui.Infof("No webhooks configured.")
		return nil
	}
	for _, v := range out.Items {
		state := "disabled"
		if v.Enabled {
			state = "enabled"
		}
		ui.Plain("  %s [%s] %s", v.ID, state, v.URL)
		if len(v.EventTypes) > 0 {
			ui.Plain("    events: %s", strings.Join(v.EventTypes, ", "))
		}
		ui.Plain("")
	}
	if len(out.Items) < out.Total {
		ui.Plain("%s", ui.Dim(fmt.Sprintf("Showing %d of %d webhooks.", len(out.Items), out.Total)))
	}
	return nil
}

func webhookCreate(ctx context.Context, d *Deps, bank, rawURL string, cf webhookCreateFlags) error {
	events := splitFlagList(cf.eventTypes)
	if len(events) == 0 {
		// Client-side default: an empty subscription becomes the
		// consolidation-completed event.
		events = []string{"consolidation.completed"}
	}
	body := webhookCreateBody{URL: rawURL, EventTypes: events, Enabled: !cf.disabled}
	if cf.secret != "" {
		body.Secret = &cf.secret
	}
	var out webhookOut
	path := fmt.Sprintf("%s/%s/webhooks", batchDBase, bank)
	if err := d.Client.Do(ctx, "POST", path, body, &out); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return d.Format.Render(d.UI.Stdout, out)
	}
	ui := d.UI
	ui.Infof("Webhook '%s' created", out.ID)
	ui.Plain("  URL: %s", out.URL)
	ui.Plain("  Events: %s", strings.Join(out.EventTypes, ", "))
	return nil
}

func webhookUpdate(ctx context.Context, d *Deps, bank, id string, f *webhookUpdateFlags) error {
	if !f.any() {
		return NewCommandError("At least one of --url, --event-types, --enabled, or --secret must be provided")
	}
	// A plain partial PATCH: only the passed fields are sent and no
	// read-back happens.
	body := webhookUpdateBody{}
	if f.url != "" {
		body.URL = &f.url
	}
	if f.eventTypes != "" {
		events := splitFlagList(f.eventTypes)
		body.EventTypes = &events
	}
	if f.enabled.set {
		v := f.enabled.value
		body.Enabled = &v
	}
	if f.secret != "" {
		body.Secret = &f.secret
	}
	var out webhookOut
	path := fmt.Sprintf("%s/%s/webhooks/%s", batchDBase, bank, id)
	if err := d.Client.Do(ctx, "PATCH", path, body, &out); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return d.Format.Render(d.UI.Stdout, out)
	}
	d.UI.Infof("Webhook '%s' updated", id)
	return nil
}

func webhookDelete(ctx context.Context, d *Deps, bank, id string, yes bool) error {
	prompt := fmt.Sprintf("Are you sure you want to delete webhook '%s'? This cannot be undone.", id)
	proceed, err := confirmDestructiveAction(d, prompt, yes)
	if err != nil || !proceed {
		return err
	}
	var out webhookDeleteOut
	path := fmt.Sprintf("%s/%s/webhooks/%s", batchDBase, bank, id)
	if err := d.Client.Do(ctx, "DELETE", path, nil, &out); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return d.Format.Render(d.UI.Stdout, out)
	}
	if out.Success {
		d.UI.Infof("Webhook '%s' deleted", id)
	} else {
		d.UI.Errorf("Failed to delete webhook")
	}
	return nil
}

func webhookDeliveries(ctx context.Context, d *Deps, bank, id, cursor string, limit triStateInt) error {
	query := url.Values{}
	if limit.set {
		query.Set("limit", strconv.Itoa(limit.value))
	}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	var out webhookDeliveriesOut
	path := fmt.Sprintf("%s/%s/webhooks/%s/deliveries", batchDBase, bank, id)
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	if err := d.Client.Do(ctx, "GET", path, nil, &out); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return d.Format.Render(d.UI.Stdout, out)
	}
	ui := d.UI
	ui.Infof("Deliveries for %s", id)
	if len(out.Items) == 0 {
		ui.Infof("No delivery attempts recorded.")
		return nil
	}
	for _, v := range out.Items {
		status := "-"
		if v.LastResponseStatus != nil {
			status = strconv.Itoa(*v.LastResponseStatus)
		}
		ui.Plain("  %s [%s] %s — attempts: %d", v.ID, v.EventType, status, v.Attempts)
		if v.LastError != nil && *v.LastError != "" {
			ui.Plain("    error: %s", *v.LastError)
		}
	}
	if out.NextCursor != nil {
		ui.Plain("")
		ui.Plain("  next cursor: %s", *out.NextCursor)
	}
	return nil
}
