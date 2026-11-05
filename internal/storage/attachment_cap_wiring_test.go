package storage

import "testing"

// An async retain replays the resolved attachment cap through its queued
// payload, so a worker chunks with the same per-chunk image limit as the
// synchronous path that accepted the write.
func TestAsyncRetainCarriesAttachmentCap(t *testing.T) {
	payload := AsyncRetainPayload("b1", "op-1", RetainRequest{
		BankID:                 "b1",
		Items:                  []RetainItem{{Content: "text"}},
		MaxAttachmentsPerChunk: 3,
	})
	req, err := DecodeAsyncRetainPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	if req.MaxAttachmentsPerChunk != 3 {
		t.Fatalf("replayed attachment cap = %d, want 3", req.MaxAttachmentsPerChunk)
	}
	opts := segmentOptionsFor(req)
	if opts.MaxAttachmentsPerChunk != 3 {
		t.Fatalf("segmentation cap = %d, want 3", opts.MaxAttachmentsPerChunk)
	}
}
