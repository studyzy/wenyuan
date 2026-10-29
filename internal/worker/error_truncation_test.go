package worker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// A failure reason longer than the error_message column is truncated to its
// width at the fail write, exactly as the reference's worker truncates it
// (worker/poller.py:952), counting characters rather than bytes.
func TestFailTruncatesErrorMessage(t *testing.T) {
	tests := []struct {
		name       string
		detail     string
		wantReason string // the prefix the stored reason must carry
	}{
		{
			name:       "ascii detail",
			detail:     strings.Repeat("x", 6000),
			wantReason: strings.Repeat("x", 5000),
		},
		{
			name:       "multibyte detail truncates by characters, not bytes",
			detail:     strings.Repeat("字", 6000),
			wantReason: strings.Repeat("字", 5000),
		},
		{
			name:       "a detail within the width passes through",
			detail:     strings.Repeat("y", 4999),
			wantReason: strings.Repeat("y", 4999),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newStore(t)
			queue(t, store, "op-1", storage.OperationTypeTransferImport)
			ctx := context.Background()
			op, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			consumer := New(store, stubProcessor{err: Permanent(errors.New(test.detail))}, Config{
				WorkerID: "worker-1", MaxRetries: 0, InitialBackoff: time.Second,
				MaxBackoff: time.Minute, Lease: time.Hour, Concurrency: 1,
				PollInterval: time.Millisecond, Logger: quietLogger(),
				Now: func() time.Time { return time.Now().UTC() }, Sleep: func(time.Duration) {},
			})
			consumer.handle(ctx, op)
			after, err := store.GetOperation(ctx, "bank-a", "op-1")
			if err != nil {
				t.Fatal(err)
			}
			if after.Status != storage.StatusFailed {
				t.Fatalf("status = %s, want failed", after.Status)
			}
			if after.Error != test.wantReason {
				t.Fatalf("error_message = %.40s… (%d runes), want the %d-rune prefix",
					after.Error, len([]rune(after.Error)), len([]rune(test.wantReason)))
			}
		})
	}
}

// A scheduled retry truncates its recorded cause the same way, so the retry
// write cannot overflow the column either (worker/poller.py:1083).
func TestScheduledRetryTruncatesErrorMessage(t *testing.T) {
	store := newStore(t)
	queue(t, store, "op-1", storage.OperationTypeTransferImport)
	ctx := context.Background()
	op, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	detail := strings.Repeat("字", 6000)
	consumer := New(store, stubProcessor{err: errors.New(detail)}, Config{
		WorkerID: "worker-1", MaxRetries: 3, InitialBackoff: time.Millisecond,
		MaxBackoff: time.Millisecond, Lease: time.Hour, Concurrency: 1,
		PollInterval: time.Millisecond, Logger: quietLogger(),
		Now: func() time.Time { return time.Now().UTC() }, Sleep: func(time.Duration) {},
	})
	consumer.handle(ctx, op)
	after, err := store.GetOperation(ctx, "bank-a", "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != storage.StatusPending || after.RetryCount != 1 {
		t.Fatalf("operation = %+v, want it scheduled for its second attempt", after)
	}
	if after.Error != strings.Repeat("字", 5000) {
		t.Fatalf("error_message holds %d characters, want the truncated 5000", len([]rune(after.Error)))
	}
	if got := after.ResultMetadata["last_retry_error"]; got != strings.Repeat("字", 5000) {
		t.Fatalf("last_retry_error holds %d characters, want the truncated 5000",
			len([]rune(got.(string))))
	}
}
