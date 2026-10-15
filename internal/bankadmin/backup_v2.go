package bankadmin

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/studyzy/wenyuan/internal/storage"
)

// binaryCopyManifestVersion is the archive format whose table members are raw
// PostgreSQL binary COPY streams (one <table>.bin per table) rather than JSON
// row arrays. Its manifest column lists are what a restore replays each
// stream against, positionally.
const binaryCopyManifestVersion = 2

// runRestoreBinary replays a binary-COPY archive onto the target. Every table
// is validated against the target's columns before anything destructive
// happens, then the whole set is replaced in one atomic step: a single
// transaction truncates and reloads, so a failure anywhere leaves the
// previous data in place.
func runRestoreBinary(ctx context.Context, caps storage.AdminTableStore, reader *zip.ReadCloser, manifest backupManifest, input string, out io.Writer) error {
	order, err := restoreOrder(manifest)
	if err != nil {
		return err
	}

	loads := make([]storage.BinaryLoad, 0, len(order))
	for _, table := range order {
		entry := manifest.Tables[table]
		data, err := readZipEntry(&reader.Reader, table+".bin")
		if err != nil {
			return fmt.Errorf("read %s: %w", table, err)
		}
		// Fail before the destructive step when a member is not a stream at
		// all; a stream whose shape disagrees with the columns its own
		// manifest declares is caught by the strip pass or, at the latest, by
		// the server decoding it.
		if _, err := checkBinaryCopyHeader(data); err != nil {
			return fmt.Errorf("%s: %w", table, err)
		}
		target, err := caps.TableColumns(ctx, table)
		if err != nil {
			return err
		}
		kept, dropped, err := planColumns(table, entry.Columns, target, out)
		if err != nil {
			return err
		}
		data, err = stripBinaryCopyFields(data, dropped, len(entry.Columns))
		if err != nil {
			return fmt.Errorf("%s: %w", table, err)
		}
		loads = append(loads, storage.BinaryLoad{Table: table, Columns: kept, Data: data})
	}

	if err := caps.RestoreBinary(ctx, loads); err != nil {
		return err
	}
	total := 0
	for _, load := range loads {
		rows := manifest.Tables[load.Table].Rows
		total += rows
		_, _ = fmt.Fprintf(out, "  restored %s: %d row(s)\n", load.Table, rows)
	}
	_, _ = fmt.Fprintf(out, "restored %d row(s) across %d table(s) from %s\n", total, len(loads), input)
	return nil
}

// planColumns matches an archive table's column list against the target's. A
// column the target no longer has is not an error — a dropped column would
// otherwise make every archive taken before it permanently unrestorable: its
// fields are stripped from the stream and the operator is told. A type change
// is fatal: the values cannot be trusted to land in the same shape. The
// returned columns are the archive's, in stream order, minus the dropped ones.
func planColumns(table string, archive, target []storage.AdminColumn, out io.Writer) ([]string, []int, error) {
	targetTypes := make(map[string]string, len(target))
	for _, column := range target {
		targetTypes[column.Name] = column.Type
	}
	var kept []string
	var dropped []int
	var mismatched []string
	for index, column := range archive {
		actual, ok := targetTypes[column.Name]
		if !ok {
			dropped = append(dropped, index)
			continue
		}
		if actual != column.Type {
			mismatched = append(mismatched, fmt.Sprintf("%s (%s in archive, %s in target)", column.Name, column.Type, actual))
			continue
		}
		kept = append(kept, column.Name)
	}
	if len(mismatched) > 0 {
		return nil, nil, fmt.Errorf("archive is incompatible with target table %q: %s", table, strings.Join(mismatched, ", "))
	}
	if len(dropped) > 0 {
		names := make([]string, 0, len(dropped))
		for _, index := range dropped {
			names = append(names, archive[index].Name)
		}
		_, _ = fmt.Fprintf(out, "  %s: ignoring %d archive column(s) absent from the target schema: %s\n",
			table, len(dropped), strings.Join(names, ", "))
	}
	return kept, dropped, nil
}
