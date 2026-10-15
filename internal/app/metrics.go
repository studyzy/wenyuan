package app

import (
	"context"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// operationMetrics reports the async-operation backlog as Prometheus gauges, so
// a scrape shows how much queued work a deployment is carrying. It is computed
// at scrape time and bounded, so a slow store cannot hang the scrape.
func operationMetrics(store storage.Store) func() map[string]float64 {
	return func() map[string]float64 {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		page, err := store.ListBanks(ctx, storage.BankQuery{Limit: 1000})
		if err != nil {
			return nil
		}
		var pending, failed float64
		for _, bank := range page.Banks {
			stats, err := store.BankStats(ctx, bank.BankID)
			if err != nil {
				continue
			}
			pending += float64(stats.PendingOperations)
			failed += float64(stats.FailedOperations)
		}
		return map[string]float64{
			"hindsight_async_operations_pending": pending,
			"hindsight_async_operations_failed":  failed,
		}
	}
}
