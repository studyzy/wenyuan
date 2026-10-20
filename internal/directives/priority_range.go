package directives

import (
	"math"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// The directive's `priority` is an unconstrained Python int in the reference's
// request model but an int4 column in the schema, so a value outside the
// column's range reaches the driver, which refuses to bind it. The endpoint has
// no handler for that failure — it is caught by the catch-all — so the caller
// sees a 500 whose detail is the driver's own text. The position it names is the
// argument's place in the statement, numbered from `$1`.

// createDirectivePriorityArg is the position `priority` rides as in the insert:
// id, bank_id, name, content, priority.
const createDirectivePriorityArg = 4

// updateDirectivePriorityArg reports the position `priority` rides as in the
// update. The reference's update builds its SET list from the members the patch
// supplied, so the number counts the ones ahead of priority in declaration
// order and nothing else — the id and bank_id that identify the row are not
// part of it. Verified against the live service: `{"priority": …}` names $1,
// `{"name":…, "priority": …}` and `{"content":…, "priority": …}` both name $2,
// and `{"name":…, "content":…, "priority": …}` names $3 with or without the
// members after it.
func updateDirectivePriorityArg(nameGiven, contentGiven bool) int {
	position := 1
	if nameGiven {
		position++
	}
	if contentGiven {
		position++
	}
	return position
}

// priorityRangeFailure answers a priority the column cannot hold with the
// driver's text, or nil when the value fits.
func priorityRangeFailure(arg, priority int) *httpx.ValidationFailure {
	if priority >= math.MinInt32 && priority <= math.MaxInt32 {
		return nil
	}
	return &httpx.ValidationFailure{
		Status: httpx.KindInternal,
		Detail: (&storage.PriorityOutOfRangeError{Arg: arg, Value: priority}).Error(),
	}
}
