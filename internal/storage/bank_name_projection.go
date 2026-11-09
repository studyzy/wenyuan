package storage

// bankNameCarriedFromID returns the select expressions that copy a bank's row
// onto its renamed identifier, with `name` handled specially.
//
// `name` is a display label, not a second identifier: an operator can set it to
// anything ("文渊" for a bank whose id is `zy::wenyuan`). A rename therefore
// leaves a chosen label alone, but a label that is still just the old
// identifier — which is what a bank created without an explicit name, and every
// bank an archive import carries, holds — would keep naming a bank that no
// longer exists. The console shows that column, so the rename would look like
// it had not happened. Such a label follows the identifier; any other label is
// carried over untouched.
//
// bankIDArgument is the placeholder bound to the new identifier, and
// sourceArgument the one bound to the old.
func bankNameCarriedFromID(columns []string, bankIDArgument, sourceArgument string) []string {
	carried := make([]string, 0, len(columns))
	for _, column := range columns {
		if column == "bank_id" {
			continue
		}
		if column == "name" {
			carried = append(carried, bankNameExpression(bankIDArgument, sourceArgument))
			continue
		}
		carried = append(carried, quoteIdent(column))
	}
	return carried
}

// bankNameInsertColumns returns the INSERT column list for a rename: the
// surviving columns of the copy, plainly named. A select expression cannot
// serve here — an INSERT column list takes identifiers, not expressions.
func bankNameInsertColumns(columns []string) []string {
	inserted := make([]string, 0, len(columns))
	for _, column := range columns {
		if column == "bank_id" {
			continue
		}
		inserted = append(inserted, quoteIdent(column))
	}
	return inserted
}

// bankNameExpression is the label the renamed row carries: the new identifier
// when the old label was the old identifier, otherwise the label itself.
func bankNameExpression(bankIDArgument, sourceArgument string) string {
	return `CASE WHEN ` + quoteIdent("name") + ` = ` + sourceArgument +
		` THEN ` + bankIDArgument + `::text ELSE ` + quoteIdent("name") + ` END`
}
