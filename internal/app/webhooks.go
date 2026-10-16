package app

import (
	"net/http"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/webhooks"
)

// mountWebhooks wires the webhook route family into the existing composition
// root, with the URL guard that keeps a registered target from reaching the
// service's own network.
func (a *App) mountWebhooks(handle func(method, pattern string, handler http.HandlerFunc)) {
	webhooks.NewHandlers(a.Store, a.webhookGuard()).BindRoutes(handle)
}

// webhookGuard builds the URL guard from the deployment's webhook settings.
// Registration and delivery share it, so the policy a target was accepted under
// is the policy a delivery is checked against.
func (a *App) webhookGuard() *webhooks.Guard {
	return NewWebhookGuard(a.Config)
}

// NewWebhookGuard builds the webhook URL guard from the resolved configuration,
// so the API's registration route and a standalone worker process's delivery
// path enforce one policy and carry one deployment-level target between them.
func NewWebhookGuard(cfg config.Config) *webhooks.Guard {
	return webhooks.NewGuard(webhooks.GuardConfig{
		AllowInsecure:     cfg.WebhookAllowInsecure,
		AllowPrivateHosts: cfg.WebhookAllowPrivateHosts,
		AllowedHosts:      cfg.WebhookAllowedHosts,
		Global:            NewGlobalTarget(cfg.WebhookGlobal),
	})
}

// NewGlobalTarget builds the server-level webhook target from the resolved
// configuration. The target is delivered alongside every bank's own
// registrations; an unset URL disables it. It is always non-nil so the
// response-body setting survives even when the URL is empty.
func NewGlobalTarget(cfg config.WebhookGlobal) *webhooks.GlobalTarget {
	return &webhooks.GlobalTarget{
		URL:                cfg.URL,
		Secret:             cfg.Secret,
		EventTypes:         cfg.EventTypes,
		ExposeResponseBody: cfg.ExposeResponseBody,
	}
}
