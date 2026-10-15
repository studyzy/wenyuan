package httpx

import (
	"net/http"
	"strings"
)

// OperationValidationMiddleware gates the operations that have something to
// validate and resolves the tag scope the caller is confined to. A denied
// operation is answered with the validator's status and reason in the shared
// error envelope and never reaches its handler. The resolved scope is carried
// on the request context so a tag-scoped handler can AND it into its own
// filter (see TagScopeFrom). A host with no validator is a pass-through, so a
// deployment that registers nothing behaves exactly as before.
func OperationValidationMiddleware(host *ExtensionHost) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			validator := host.operationValidator()
			credential := credentialFromRequest(r)
			// The credential rides on the context so a validator that resolves a
			// scope from the API key can read it.
			r = r.WithContext(WithCredential(r.Context(), credential))
			// A bank write is gated even when the operation itself is not: the
			// question is whether this credential may touch this bank, and a
			// validator that carries the rule answers it before the handler
			// ever queues work.
			if gate, ok := validator.(BankWriteGate); ok {
				if bankID, isWrite := bankWritePath(r); isWrite {
					decision := gate.ValidateBankWrite(r.Context(), bankID, credential)
					if !decision.Allowed {
						status := decision.Status
						if status == 0 {
							status = KindForbidden
						}
						WriteErrorFor(w, r, NewHTTPError(status, decision.Reason))
						return
					}
				}
			}
			if validator == nil {
				next.ServeHTTP(w, r)
				return
			}
			if op, bankID, ok := classifyOperation(r); ok {
				decision := validator.ValidateOperation(r.Context(), OperationRequest{Operation: op, BankID: bankID})
				if !decision.Allowed {
					status := decision.Status
					if status == 0 {
						status = KindForbidden
					}
					WriteErrorFor(w, r, NewHTTPError(status, decision.Reason))
					return
				}
			}
			// The caller's read scope is resolved for every bank-scoped request,
			// not only the ones the validator gates: a scoped reader must be
			// confined on the by-id and listing reads too (a document, a memory,
			// an entity), and each of those handlers consumes the scope from the
			// request. A handler that does not read it is unaffected.
			if bankID, ok := bankScopedBank(r); ok {
				if scope := validator.ResolveTagScope(r.Context(), bankID); len(scope) > 0 {
					r = WithTagScope(r, scope)
				}
				if resolver, ok := validator.(TagWriteScopeResolver); ok {
					if tags, confined := resolver.ResolveWriteTagScope(r.Context(), bankID); confined {
						r = WithWriteTagScope(r, tags)
					}
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// classifyOperation maps a bank-scoped request onto the operation the validator
// gates. Only the operations with something to validate are classified; every
// other request passes through untouched.
func classifyOperation(r *http.Request) (operation, bankID string, ok bool) {
	if r.Method != http.MethodPost {
		return "", "", false
	}
	const prefix = PrefixBanksV1 + "/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		return "", "", false
	}
	bankID, suffix, found := strings.Cut(r.URL.Path[len(prefix):], "/")
	if !found || bankID == "" {
		return "", "", false
	}
	switch "/" + suffix {
	case SuffixMemories, SuffixFileRetain:
		return OperationRetain, bankID, true
	case SuffixRecall:
		return OperationRecall, bankID, true
	case SuffixReflect:
		return OperationReflect, bankID, true
	default:
		return "", "", false
	}
}

// tagScopeKey keys the caller's resolved tag scope on the request context.
type tagScopeKey struct{}

// WithTagScope records the caller's resolved tag scope on the request.
func WithTagScope(r *http.Request, scope []TagGroup) *http.Request {
	return r.WithContext(contextWithValue(r.Context(), tagScopeKey{}, scope))
}

// TagScopeFrom returns the caller's resolved tag scope, or nil when the
// request carries none.
func TagScopeFrom(r *http.Request) []TagGroup {
	scope, _ := r.Context().Value(tagScopeKey{}).([]TagGroup)
	return scope
}
