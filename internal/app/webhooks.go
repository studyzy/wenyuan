package app

import (
	"net/http"

	"github.com/studyzy/wenyuan/internal/webhooks"
)

// mountWebhooks wires the webhook route family into the existing composition
// root, with the URL guard that keeps a registered target from reaching the
// service's own network.
func (a *App) mountWebhooks(handle func(method, pattern string, handler http.HandlerFunc)) {
	guard := webhooks.NewGuard(webhooks.GuardConfig{
		AllowInsecure:     a.Config.WebhookAllowInsecure,
		AllowPrivateHosts: a.Config.WebhookAllowPrivateHosts,
		AllowedHosts:      a.Config.WebhookAllowedHosts,
	})
	webhooks.NewHandlers(a.Store, guard).BindRoutes(handle)
}
