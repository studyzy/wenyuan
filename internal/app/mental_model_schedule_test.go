package app

import (
	"context"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/tenant"
)

// The full-refresh arm answers to its own clock: a model whose full_refresh_cron
// fired after its last full refresh is due even when a delta ran a moment ago,
// and one whose full schedule has not fired since its last full refresh is not
// due however stale it is.
func TestDueScheduledRefreshFullArm(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	const everyFive = "*/5 * * * *"
	const yearly = "0 0 1 1 *"
	cases := []struct {
		name     string
		entry    storage.ScheduledMentalModel
		wantDue  bool
		wantFull bool
	}{
		{
			name:     "full cron due, last full a day ago, a delta an hour ago",
			entry:    storage.ScheduledMentalModel{FullRefreshCron: everyFive, LastFullRefreshedAt: now.Add(-24 * time.Hour), LastRefreshedAt: now.Add(-time.Hour)},
			wantDue:  true,
			wantFull: true,
		},
		{
			name:    "full cron due but nothing refreshed since the last full one",
			entry:   storage.ScheduledMentalModel{FullRefreshCron: everyFive, LastFullRefreshedAt: now.Add(-24 * time.Hour), LastRefreshedAt: now.Add(-24 * time.Hour)},
			wantDue: false,
		},
		{
			name:     "full cron due and never refreshed at all",
			entry:    storage.ScheduledMentalModel{FullRefreshCron: everyFive},
			wantDue:  true,
			wantFull: true,
		},
		{
			name:     "a recent delta refresh does not push the full schedule back",
			entry:    storage.ScheduledMentalModel{FullRefreshCron: everyFive, LastFullRefreshedAt: now.Add(-24 * time.Hour), LastRefreshedAt: now.Add(-5 * time.Second)},
			wantDue:  true,
			wantFull: true,
		},
		{
			name:    "a yearly full refresh that ran seconds ago is not due",
			entry:   storage.ScheduledMentalModel{FullRefreshCron: yearly, LastFullRefreshedAt: now.Add(-5 * time.Second), LastRefreshedAt: now.Add(-time.Second)},
			wantDue: false,
		},
		{
			name:     "full cron due counts without last_full_refreshed_at being set",
			entry:    storage.ScheduledMentalModel{FullRefreshCron: everyFive, LastRefreshedAt: now.Add(-time.Hour)},
			wantDue:  true,
			wantFull: true,
		},
		{
			name:    "plain cron due, no full schedule",
			entry:   storage.ScheduledMentalModel{RefreshCron: everyFive, LastRefreshedAt: now.Add(-time.Hour)},
			wantDue: true,
		},
		{
			name:    "plain cron not due",
			entry:   storage.ScheduledMentalModel{RefreshCron: yearly, LastRefreshedAt: now.Add(-time.Minute)},
			wantDue: false,
		},
		{
			name: "both schedules due: one refresh, in full mode",
			entry: storage.ScheduledMentalModel{
				RefreshCron: everyFive, LastRefreshedAt: now.Add(-time.Hour),
				FullRefreshCron: everyFive, LastFullRefreshedAt: now.Add(-24 * time.Hour),
			},
			wantDue:  true,
			wantFull: true,
		},
		{
			name:    "no schedule at all",
			entry:   storage.ScheduledMentalModel{LastRefreshedAt: now.Add(-time.Hour)},
			wantDue: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			due, full := dueScheduledRefresh(tc.entry, now)
			if due != tc.wantDue || full != tc.wantFull {
				t.Fatalf("dueScheduledRefresh = (due %v, full %v), want (%v, %v)", due, full, tc.wantDue, tc.wantFull)
			}
		})
	}
}

// The scheduled job submits a full_refresh_cron tick in full mode, and skips it
// while the model's last refresh failed — the same park every automatic trigger
// honours.
func TestScheduledFullRefreshJob(t *testing.T) {
	const everyFive = "*/5 * * * *"
	cases := []struct {
		name      string
		fullCron  string
		lastFull  time.Duration
		last      time.Duration
		failed    bool
		wantQueue bool
		wantFull  bool
	}{
		{
			name: "due full refresh is queued in full mode", fullCron: everyFive,
			lastFull: 24 * time.Hour, last: time.Hour, wantQueue: true, wantFull: true,
		},
		{
			name: "nothing refreshed since the last full one", fullCron: everyFive,
			lastFull: 24 * time.Hour, last: 24 * time.Hour, wantQueue: false,
		},
		{
			name: "a parked model is skipped", fullCron: everyFive,
			lastFull: 24 * time.Hour, last: time.Hour, failed: true, wantQueue: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now().UTC()
			store := newJobStore(t)
			ctx := context.Background()
			if _, err := store.CreateBank(ctx, storage.Bank{BankID: "b1", Name: "b1"}); err != nil {
				t.Fatal(err)
			}
			_, err := store.PutMentalModel(ctx, storage.MentalModel{
				ID: "mm-1", BankID: "b1", Subtype: storage.SubtypePinned, Name: "m", SourceQuery: "q",
				Trigger:             map[string]any{"mode": "delta", "full_refresh_cron": tc.fullCron},
				LastRefreshedAt:     now.Add(-tc.last),
				LastFullRefreshedAt: timePtr(now.Add(-tc.lastFull)),
			})
			if err != nil {
				t.Fatal(err)
			}
			if tc.failed {
				// A failed refresh parks the model against every automatic trigger.
				if err := store.RecordMentalModelRefreshFailure(ctx, "b1", "mm-1", now.Add(-time.Minute)); err != nil {
					t.Fatal(err)
				}
			}
			if err := refreshDueMentalModels(ctx, store, tenant.NewDefault("public"), newMentalModelRefresh(store, nil, nil)); err != nil {
				t.Fatalf("job: %v", err)
			}
			queued := queuedRefreshPayloads(t, store, "mm-1")
			if !tc.wantQueue {
				if len(queued) != 0 {
					t.Fatalf("queued %d refreshes, want none: %+v", len(queued), queued)
				}
				return
			}
			if len(queued) != 1 {
				t.Fatalf("queued %d refreshes, want exactly one: %+v", len(queued), queued)
			}
			gotFull := queued[0][storage.RefreshModePayloadKey] == "full"
			if gotFull != tc.wantFull {
				t.Fatalf("queued mode = %v, want full = %v (payload %+v)", queued[0][storage.RefreshModePayloadKey], tc.wantFull, queued[0])
			}
		})
	}
}

// timePtr and stringPtr keep the schedule seeds readable.
func timePtr(at time.Time) *time.Time { return &at }

func stringPtr(value string) *string { return &value }

// queuedRefreshPayloads returns the payload of every refresh of a model that is
// still pending or processing.
func queuedRefreshPayloads(t *testing.T, store storage.Store, modelID string) []map[string]any {
	t.Helper()
	out := make([]map[string]any, 0)
	ops, err := store.ListOperations(context.Background(), storage.OperationQuery{BankID: "b1"})
	if err != nil {
		t.Fatalf("list operations: %v", err)
	}
	for _, op := range ops.Operations {
		if op.OperationType != storage.OperationTypeRefreshMentalModel {
			continue
		}
		if op.Payload[storage.MentalModelIDPayloadKey] != modelID {
			continue
		}
		if op.Status != storage.StatusPending && op.Status != storage.StatusProcessing {
			continue
		}
		out = append(out, op.Payload)
	}
	return out
}

// A full-mode refresh stamps last_full_refreshed_at and leaves last_refreshed_at
// alone to say "a refresh finished"; a delta refresh moves the second and never
// the first. The one-run override does not touch the stored trigger.
func TestRefreshStampsTheFullRefreshClock(t *testing.T) {
	store, modelID := refreshFixture(t, "deployment")
	ctx := context.Background()
	if _, err := store.UpdateMentalModel(ctx, "b1", modelID, storage.MentalModelPatch{
		Trigger: &map[string]any{"mode": "delta"},
	}); err != nil {
		t.Fatal(err)
	}
	// Give the model content so a delta is actually taken.
	if _, err := store.UpdateMentalModel(ctx, "b1", modelID, storage.MentalModelPatch{
		Content: stringPtr("deployments happen on tuesdays"), LastRefreshedSourceQuery: stringPtr("deployment"),
	}); err != nil {
		t.Fatal(err)
	}
	runner := newMentalModelRefresh(store, nil, nil)

	if _, report, err := runner.refresh(ctx, "b1", modelID, ""); err != nil {
		t.Fatalf("delta refresh: %v", err)
	} else if report.modeFallbackReason != "" {
		t.Fatalf("delta fallback = %q, want none", report.modeFallbackReason)
	}
	if refreshed, _ := store.GetMentalModel(ctx, "b1", modelID); refreshed.LastFullRefreshedAt != nil {
		t.Fatalf("a delta refresh stamped last_full_refreshed_at = %v", refreshed.LastFullRefreshedAt)
	}

	if _, report, err := runner.refresh(ctx, "b1", modelID, refreshModeFull); err != nil {
		t.Fatalf("full refresh: %v", err)
	} else if report.modeFallbackReason != "" {
		t.Fatalf("an asked-for full refresh reported a fallback: %q", report.modeFallbackReason)
	}
	refreshed, err := store.GetMentalModel(ctx, "b1", modelID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.LastFullRefreshedAt == nil {
		t.Fatal("a full-mode refresh must stamp last_full_refreshed_at")
	}
	// The override is one run: the stored trigger keeps its delta mode.
	if mode, _ := refreshed.Trigger["mode"].(string); mode != "delta" {
		t.Fatalf("stored trigger mode = %q, want delta: the override is one run only", mode)
	}
}

// A one-run full override on a delta model reads its whole scope and is not
// reported as a fallback: the caller asked for exactly that.
func TestFullOverrideTakesTheFullPath(t *testing.T) {
	delta := map[string]any{"mode": "delta"}
	model := storage.MentalModel{
		Trigger: delta, Content: "written", LastRefreshedSourceQuery: "q", SourceQuery: "q",
	}
	if mode, reason := effectiveRefreshMode(model, refreshModeFull); mode != "full" || reason != "" {
		t.Fatalf("override = (%q, %q), want (full, none)", mode, reason)
	}
	if mode, _ := effectiveRefreshMode(model, ""); mode != "delta" {
		t.Fatalf("no override = %q, want the model's own delta", mode)
	}
}
