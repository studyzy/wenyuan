package reporting

import "net/http"

// PathBankID is the bank identifier a bank-scoped path names, verbatim.
//
// The reference declares the parameter as a plain `str` and binds it straight
// into its queries, so the identifier is whatever the client sent: no trimming,
// no case folding. Surrounding whitespace therefore names a bank nobody created
// (the not-found detail echoes it back, quotes and all), and an alias lookup is
// exact. Only the empty identifier is special — no route pattern matches it, so
// reaching a handler with "" means the path had nothing there, which is the
// reference's own "Bank ... not found".
func PathBankID(r *http.Request) string { return r.PathValue("bank_id") }

// pathID is a path parameter read verbatim: the same rule as PathBankID for an
// identifier that is not a bank.
func pathID(r *http.Request, name string) string { return r.PathValue(name) }
