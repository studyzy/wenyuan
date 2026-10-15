package cli

import (
	"context"
	"fmt"
	"strings"
)

// batchDBase is the bank-scoped API path prefix shared by the resource
// families of this batch.
const batchDBase = "/v1/default/banks"

// opEntry is one operation list entry. The list keeps null fields, so the
// nullable members serialize as null.
type opEntry struct {
	ID            string         `json:"id" yaml:"id"`
	TaskType      string         `json:"task_type" yaml:"task_type"`
	OperationID   *string        `json:"operation_id" yaml:"operation_id"`
	OperationType *string        `json:"operation_type" yaml:"operation_type"`
	ItemsCount    int            `json:"items_count" yaml:"items_count"`
	DocumentID    *string        `json:"document_id" yaml:"document_id"`
	Filename      *string        `json:"filename" yaml:"filename"`
	MentalModelID *string        `json:"mental_model_id" yaml:"mental_model_id"`
	Details       map[string]any `json:"details" yaml:"details"`
	CreatedAt     string         `json:"created_at" yaml:"created_at"`
	UpdatedAt     *string        `json:"updated_at" yaml:"updated_at"`
	Status        string         `json:"status" yaml:"status"`
	ErrorMessage  *string        `json:"error_message" yaml:"error_message"`
	RetryCount    int            `json:"retry_count" yaml:"retry_count"`
	NextRetryAt   *string        `json:"next_retry_at" yaml:"next_retry_at"`
	Progress      map[string]any `json:"progress" yaml:"progress"`
}

// opListOut is the operation listing envelope.
type opListOut struct {
	BankID     string    `json:"bank_id" yaml:"bank_id"`
	Total      int       `json:"total" yaml:"total"`
	Limit      int       `json:"limit" yaml:"limit"`
	Offset     int       `json:"offset" yaml:"offset"`
	Operations []opEntry `json:"operations" yaml:"operations"`
}

// opStatusOut is the single-operation read. Unlike the list entry it carries
// result_metadata, the child roll-up and the raw task payload; null fields
// are dropped.
type opStatusOut struct {
	OperationID     string         `json:"operation_id" yaml:"operation_id"`
	Status          string         `json:"status" yaml:"status"`
	OperationType   *string        `json:"operation_type,omitempty" yaml:"operation_type,omitempty"`
	ID              *string        `json:"id,omitempty" yaml:"id,omitempty"`
	TaskType        *string        `json:"task_type,omitempty" yaml:"task_type,omitempty"`
	MentalModelID   *string        `json:"mental_model_id,omitempty" yaml:"mental_model_id,omitempty"`
	CreatedAt       *string        `json:"created_at,omitempty" yaml:"created_at,omitempty"`
	UpdatedAt       *string        `json:"updated_at,omitempty" yaml:"updated_at,omitempty"`
	CompletedAt     *string        `json:"completed_at,omitempty" yaml:"completed_at,omitempty"`
	ErrorMessage    *string        `json:"error_message,omitempty" yaml:"error_message,omitempty"`
	RetryCount      *int           `json:"retry_count,omitempty" yaml:"retry_count,omitempty"`
	NextRetryAt     *string        `json:"next_retry_at,omitempty" yaml:"next_retry_at,omitempty"`
	Progress        map[string]any `json:"progress,omitempty" yaml:"progress,omitempty"`
	ResultMetadata  map[string]any `json:"result_metadata,omitempty" yaml:"result_metadata,omitempty"`
	Details         map[string]any `json:"details,omitempty" yaml:"details,omitempty"`
	ChildOperations []any          `json:"child_operations,omitempty" yaml:"child_operations,omitempty"`
	TaskPayload     map[string]any `json:"task_payload,omitempty" yaml:"task_payload,omitempty"`
}

// opActionOut is the cancel/retry/delete acknowledgement.
type opActionOut struct {
	Success     bool   `json:"success" yaml:"success"`
	Message     string `json:"message" yaml:"message"`
	OperationID string `json:"operation_id" yaml:"operation_id"`
}

// RegisterOperationFamily wires the operation commands onto the registry.
func RegisterOperationFamily(r *Registry) {
	op := r.Sub("operation", "manage asynchronous operations")

	list := op.Sub("list", "list operations of a bank")
	list.Action(func(ctx context.Context, d *Deps) error {
		if len(d.Args) != 1 {
			return NewUsageError("operation list requires exactly one <bank_id>")
		}
		return operationList(ctx, d, d.Args[0])
	})

	get := op.Sub("get", "show one operation")
	get.Action(func(ctx context.Context, d *Deps) error {
		if len(d.Args) != 2 {
			return NewUsageError("operation get requires <bank_id> and <operation_id>")
		}
		return operationGet(ctx, d, d.Args[0], d.Args[1])
	})

	cancel := op.Sub("cancel", "cancel a pending operation")
	cancel.Action(func(ctx context.Context, d *Deps) error {
		if len(d.Args) != 2 {
			return NewUsageError("operation cancel requires <bank_id> and <operation_id>")
		}
		return operationCancel(ctx, d, d.Args[0], d.Args[1])
	})

	retry := op.Sub("retry", "requeue a failed operation")
	retry.Action(func(ctx context.Context, d *Deps) error {
		if len(d.Args) != 2 {
			return NewUsageError("operation retry requires <bank_id> and <operation_id>")
		}
		return operationRetry(ctx, d, d.Args[0], d.Args[1])
	})

	del := op.Sub("delete", "permanently delete a terminal operation")
	delYes := del.Flags().Bool("yes", false, "skip the confirmation prompt")
	del.Flags().BoolVar(delYes, "y", false, "skip the confirmation prompt (short)")
	del.Action(func(ctx context.Context, d *Deps) error {
		if len(d.Args) != 2 {
			return NewUsageError("operation delete requires <bank_id> and <operation_id>")
		}
		return operationDelete(ctx, d, d.Args[0], d.Args[1], *delYes)
	})
}

// styledOperationStatus colors a status value for pretty rendering. The
// grouping is cosmetic; the strings stay verbatim.
func styledOperationStatus(ui *UI, status string) string {
	switch status {
	case "completed", "failed", "not_found":
		return ui.paint(status, brandStart)
	case "pending", "processing":
		return ui.paint(status, brightCyan)
	case "cancelled":
		return ui.paint(status, dim)
	}
	return status
}

// operationList implements `operation list`. The CLI sends no filters: the
// endpoint's status/type/limit/offset parameters stay unset.
func operationList(ctx context.Context, d *Deps, bank string) error {
	var out opListOut
	path := fmt.Sprintf("%s/%s/operations", batchDBase, bank)
	if err := d.Client.Do(ctx, "GET", path, nil, &out); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return d.Format.Render(d.UI.Stdout, out)
	}
	ui := d.UI
	if len(out.Operations) == 0 {
		ui.Infof("No operations found")
		return nil
	}
	ui.Infof("Found %d operation(s)", len(out.Operations))
	for _, op := range out.Operations {
		ui.Plain("  Operation ID: %s", op.ID)
		ui.Plain("    Type: %s", op.TaskType)
		ui.Plain("    Status: %s", styledOperationStatus(ui, op.Status))
		ui.Plain("    Items: %d", op.ItemsCount)
		if op.Filename != nil {
			ui.Plain("    Filename: %s", *op.Filename)
		}
		if op.DocumentID != nil {
			ui.Plain("    Document ID: %s", *op.DocumentID)
		}
	}
	return nil
}

// operationGet implements `operation get`. A missing operation is a 200 whose
// status reads not_found, rendered as a normal status, not an error.
func operationGet(ctx context.Context, d *Deps, bank, opID string) error {
	var out opStatusOut
	path := fmt.Sprintf("%s/%s/operations/%s", batchDBase, bank, opID)
	if err := d.Client.Do(ctx, "GET", path, nil, &out); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return d.Format.Render(d.UI.Stdout, out)
	}
	ui := d.UI
	ui.Infof("Operation: %s", opID)
	ui.Plain("  Status: %s", styledOperationStatus(ui, out.Status))
	if out.ErrorMessage != nil && *out.ErrorMessage != "" {
		ui.Plain("  Error: %s", *out.ErrorMessage)
	}
	return nil
}

// operationCancel implements `operation cancel`: a DELETE on the member
// itself, with no confirmation prompt.
func operationCancel(ctx context.Context, d *Deps, bank, opID string) error {
	var out opActionOut
	path := fmt.Sprintf("%s/%s/operations/%s", batchDBase, bank, opID)
	if err := d.Client.Do(ctx, "DELETE", path, nil, &out); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return d.Format.Render(d.UI.Stdout, out)
	}
	if out.Success {
		d.UI.Infof("Operation cancelled successfully")
	} else {
		d.UI.Errorf("Failed to cancel operation")
	}
	return nil
}

// operationRetry implements `operation retry`: a body-less POST to the retry
// route. Pretty mode appends the response as pretty-printed JSON, the one
// pretty view that dumps raw JSON.
func operationRetry(ctx context.Context, d *Deps, bank, opID string) error {
	var out opActionOut
	path := fmt.Sprintf("%s/%s/operations/%s/retry", batchDBase, bank, opID)
	if err := d.Client.Do(ctx, "POST", path, nil, &out); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return d.Format.Render(d.UI.Stdout, out)
	}
	d.UI.Infof("Operation '%s' retried", opID)
	return FormatJSON.Render(d.UI.Stdout, out)
}

// operationDelete implements `operation delete`: a DELETE on the member with
// the /delete suffix, the permanent-removal route. Plain DELETE on the member
// is cancel, not delete.
func operationDelete(ctx context.Context, d *Deps, bank, opID string, yes bool) error {
	prompt := fmt.Sprintf("Are you sure you want to permanently delete operation '%s'? This cannot be undone.", opID)
	proceed, err := confirmDestructiveAction(d, prompt, yes)
	if err != nil || !proceed {
		return err
	}
	var out opActionOut
	path := fmt.Sprintf("%s/%s/operations/%s/delete", batchDBase, bank, opID)
	if err := d.Client.Do(ctx, "DELETE", path, nil, &out); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return d.Format.Render(d.UI.Stdout, out)
	}
	if out.Success {
		d.UI.Infof("%s", out.Message)
	} else {
		d.UI.Errorf("%s", out.Message)
	}
	return nil
}

// confirmDestructiveAction gates a destructive command. Machine formats skip
// the prompt entirely and never require --yes; pretty mode without --yes asks
// and prints the decline line without any HTTP call when the answer is not
// affirmative.
func confirmDestructiveAction(d *Deps, prompt string, yes bool) (bool, error) {
	if d.Format.IsMachine() || yes {
		return true, nil
	}
	_, _ = fmt.Fprintf(d.UI.Stdout, "%s [y/N]: ", prompt)
	answer := strings.ToLower(readLine(d.Stdin))
	if answer != "y" && answer != "yes" {
		d.UI.Infof("Operation cancelled")
		return false, nil
	}
	return true, nil
}
