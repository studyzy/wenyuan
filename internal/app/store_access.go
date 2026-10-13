package app

import (
	"context"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/storage"
)

// OpenStore resolves and opens the configured storage backend. It exists so a
// process that owns a queue but serves no HTTP surface can reuse exactly the
// same backend selection, configuration, and migration behavior as the server
// rather than duplicating it.
func OpenStore(ctx context.Context, cfg config.Config, deps Dependencies) (storage.Store, error) {
	store, err := resolveStore(ctx, cfg, deps)
	if err != nil {
		return nil, err
	}
	if cfg.RunMigrationsOnStartup {
		if err := store.Migrate(ctx); err != nil {
			return nil, err
		}
	}
	return store, nil
}
