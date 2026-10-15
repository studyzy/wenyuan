package cli

// Bank template commands: export-template, import-template and the shared
// template schema.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// registerBankTemplate wires the template commands.
func registerBankTemplate(bank *SubCmd) {
	export := bank.Sub("export-template", "export a bank template manifest")
	exportOut := export.Flags().String("out", "", "write the manifest to this file instead of stdout")
	export.Flags().StringVar(exportOut, "o", "", "write the manifest to this file (short)")
	export.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("bank export-template", d.Args, 1); err != nil {
			return err
		}
		return bankTemplateExport(ctx, d, d.Args[0], *exportOut)
	})

	importCmd := bank.Sub("import-template", "import a bank template manifest")
	importDryRun := importCmd.Flags().Bool("dry-run", false, "validate only, without applying")
	importCmd.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("bank import-template", d.Args, 2); err != nil {
			return err
		}
		return bankTemplateImport(ctx, d, d.Args[0], d.Args[1], *importDryRun)
	})

	schema := bank.Sub("template-schema", "show the bank template JSON schema")
	schema.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("bank template-schema", d.Args, 0); err != nil {
			return err
		}
		return bankTemplateSchema(ctx, d)
	})
}

// bankTemplateImportResponse is the import/dry-run answer, decoded for the
// pretty branches; machine output passes the raw payload through.
type bankTemplateImportResponse struct {
	BankID              string   `json:"bank_id"`
	ConfigApplied       bool     `json:"config_applied"`
	MentalModelsCreated []string `json:"mental_models_created"`
	MentalModelsUpdated []string `json:"mental_models_updated"`
	DirectivesCreated   []string `json:"directives_created"`
	DirectivesUpdated   []string `json:"directives_updated"`
	OperationIDs        []string `json:"operation_ids"`
	DryRun              bool     `json:"dry_run"`
}

// bankIndentedJSON re-indents a raw JSON payload with a 2-space indent and
// writes it to w.
func bankIndentedJSON(w io.Writer, raw json.RawMessage) error {
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return &DecodeError{Reason: err.Error(), Body: string(raw)}
	}
	out.WriteByte('\n')
	_, err := io.WriteString(w, out.String())
	return err
}

// bankTemplateExport implements `bank export-template`. The manifest is an
// open object, so it is carried as raw JSON and re-rendered verbatim.
func bankTemplateExport(ctx context.Context, d *Deps, bankID, outPath string) error {
	var raw json.RawMessage
	if err := d.Client.Do(ctx, "GET", bankPath(bankID, "/export"), nil, &raw); err != nil {
		return err
	}
	if outPath != "" {
		var buf bytes.Buffer
		if err := bankIndentedJSON(&buf, raw); err != nil {
			return err
		}
		if err := os.WriteFile(outPath, buf.Bytes(), 0o644); err != nil {
			return fmt.Errorf("failed to write %s: %w", outPath, err)
		}
		if !d.Format.IsMachine() {
			d.UI.Infof("Template written to %s", outPath)
		}
		return nil
	}
	if !d.Format.IsMachine() {
		return bankIndentedJSON(d.UI.Stdout, raw)
	}
	return renderRaw(d, raw)
}

// bankTemplateImport implements `bank import-template`. The file is read
// and parsed before any network traffic; a non-2xx answer aborts with the
// import-specific failure text. Verbose mode prints the full request URL.
func bankTemplateImport(ctx context.Context, d *Deps, bankID, manifestPath string, dryRun bool) error {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", manifestPath, err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("invalid JSON in %s: %w", manifestPath, err)
	}
	path := bankPath(bankID, "/import")
	if dryRun {
		path += "?dry_run=true"
	}
	if d.Verbose {
		_, _ = fmt.Fprintf(d.UI.Stderr, "POST %s\n", joinURL(d.Client.BaseURL, path))
	}
	var raw json.RawMessage
	if err := d.Client.Do(ctx, "POST", path, json.RawMessage(data), &raw); err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			return fmt.Errorf("import failed (%s): %s", apiErr.Status, apiErr.Body)
		}
		return err
	}
	if d.Format.IsMachine() {
		return renderRaw(d, raw)
	}
	var resp bankTemplateImportResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return &DecodeError{Reason: err.Error(), Body: string(raw)}
	}
	ui := d.UI
	if dryRun {
		ui.Infof("Template for bank '%s' validated", resp.BankID)
	} else {
		ui.Infof("Template imported into bank '%s'", resp.BankID)
	}
	ui.Plain("  directives created: %s", quotedList(resp.DirectivesCreated))
	ui.Plain("  directives updated: %s", quotedList(resp.DirectivesUpdated))
	ui.Plain("  mental models created: %s", quotedList(resp.MentalModelsCreated))
	ui.Plain("  mental models updated: %s", quotedList(resp.MentalModelsUpdated))
	ui.Plain("  config applied: %t", resp.ConfigApplied)
	return nil
}

// quotedList renders a string list as a bracketed, quoted element list.
func quotedList(items []string) string {
	quoted := make([]string, 0, len(items))
	for _, item := range items {
		quoted = append(quoted, strconv.Quote(item))
	}
	return "[" + strings.Join(quoted, " ") + "]"
}

// bankTemplateSchema implements `bank template-schema`.
func bankTemplateSchema(ctx context.Context, d *Deps) error {
	var raw json.RawMessage
	if err := d.Client.Do(ctx, "GET", "/v1/bank-template-schema", nil, &raw); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return renderRaw(d, raw)
	}
	return bankIndentedJSON(d.UI.Stdout, raw)
}
