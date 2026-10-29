package validation

// TagsMatchModes is the tags_match vocabulary the v1.1.0 document enumerates
// on /memories/list, /graph, /entities, /entities/graph, /entities/{id},
// /knowledge-base/tree, /knowledge-base/search and /documents. The three modes
// that name a narrower set (`any`/`all`/`exact`) are the older literal, which
// /mental-models and /directives still declare.
//
// It lives here, rather than in each handler package, because five route
// packages validate this one parameter and the served document is what defines
// the answer: a surface that accepts a subset of the enumeration answers 422 to
// a request the reference serves.
var TagsMatchModes = []string{"any", "all", "any_strict", "all_strict", "exact"}
