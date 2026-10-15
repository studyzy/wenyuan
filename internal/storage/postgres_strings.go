package storage

import "strings"

// requireStandardConformingStrings refuses a server whose
// `standard_conforming_strings` setting is off.
//
// Two statements in this package cannot take bind parameters — a `CREATE INDEX`
// is a utility statement, and the compound tag predicate is assembled from an
// arbitrary tree — so they render client-supplied values as quoted literals and
// escape a quote by doubling it. That escaping is exactly right when the server
// treats a backslash as a literal character, which is the default since
// PostgreSQL 9.1, and wrong when it does not. A deployment that turned the
// setting off would silently mis-escape, so it is refused at startup instead.
func requireStandardConformingStrings(row rowScanner) error {
	var setting string
	if err := row.Scan(&setting); err != nil {
		return err
	}
	if strings.EqualFold(strings.TrimSpace(setting), "on") {
		return nil
	}
	return ErrNonStandardStringEscaping
}

// ErrNonStandardStringEscaping reports a server configuration this package
// cannot render literals under.
var ErrNonStandardStringEscaping = errNonStandardStringEscaping{}

type errNonStandardStringEscaping struct{}

func (errNonStandardStringEscaping) Error() string {
	return "the PostgreSQL server reports standard_conforming_strings=off, which this build cannot render SQL literals under; " +
		"set standard_conforming_strings=on (the PostgreSQL default) and restart"
}
