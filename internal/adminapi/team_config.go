package adminapi

import (
	"context"

	"github.com/studyzy/wenyuan/internal/storage"
)

// TeamConfig is what the management plane's team routes need beside the tenant
// registry.
//
// Teams is optional: a deployment that does not configure it answers every
// /admin/v1/teams route with 501, which is what a single-tenant process wants.
// Banks is optional too, but only one route needs it — deleting a project
// deletes the banks the project owns — so a registry without a bank handle still
// serves every other team route.
type TeamConfig struct {
	// Teams stores teams, their members and their projects.
	Teams storage.TeamRegistry
	// Banks is the management plane's handle on a team's banks: listing the
	// banks under a project's name and deleting them.
	Banks BankAdmin
}

// BankAdmin is the management plane's handle on a team's memory banks.
//
// It is deliberately narrow: a project owns the banks whose ids start with
// "<team>::<project name>", so the plane needs to enumerate them by prefix and
// delete each one. Creating the project's own bank is a provisioning step that
// happens where a bank is ensured, not here.
type BankAdmin interface {
	// ListBanks returns the ids of the banks in schema whose id starts with
	// prefix, in id order.
	ListBanks(ctx context.Context, schema, prefix string) ([]string, error)
	// DeleteBank removes one bank and its content, reporting what it removed.
	DeleteBank(ctx context.Context, schema, bankID string) (int, error)
}
