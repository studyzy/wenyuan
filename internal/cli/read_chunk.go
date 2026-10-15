package cli

// The `chunk` family: chunk ids are globally unique, so the route is not
// bank-scoped.

import (
	"context"
	"encoding/json"
)

// chunkPath is the non-bank-scoped chunk namespace; the id spans the path
// remainder, so ids containing slashes resolve.
func chunkPath(chunkID string) string {
	return "/v1/default/chunks/" + chunkID
}

func registerChunkCommands(parent *SubCmd) {
	registerChunkGet(parent.Sub("get", "show one chunk"))
}

func registerChunkGet(parent *SubCmd) {
	parent.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("chunk get", d.Args, 1); err != nil {
			return err
		}
		var raw json.RawMessage
		if err := d.Client.Do(ctx, "GET", chunkPath(d.Args[0]), nil, &raw); err != nil {
			return err
		}
		if d.Format.IsMachine() {
			return renderRaw(d, raw)
		}
		var body struct {
			ChunkID    string  `json:"chunk_id"`
			DocumentID *string `json:"document_id"`
			BankID     string  `json:"bank_id"`
			ChunkIndex int     `json:"chunk_index"`
			ChunkText  string  `json:"chunk_text"`
			CreatedAt  string  `json:"created_at"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return &DecodeError{Reason: err.Error(), Body: string(raw)}
		}
		ui := d.UI
		ui.Plain("Chunk: %s", d.Args[0])
		ui.Plain("  ID: %s", body.ChunkID)
		ui.Plain("  Index: %d", body.ChunkIndex)
		if body.DocumentID != nil && *body.DocumentID != "" {
			ui.Plain("  Document: %s", *body.DocumentID)
		}
		ui.Plain("  Bank: %s", body.BankID)
		ui.Plain("  Created: %s", body.CreatedAt)
		ui.Plain("")
		ui.Plain("%s", ui.rule("Content"))
		ui.Plain("")
		ui.Plain("%s", body.ChunkText)
		ui.Plain("")
		return nil
	})
}
