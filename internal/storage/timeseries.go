package storage

import "time"

// TimeseriesWindow is the bucketing one period name resolves to: the bucket
// width, the distance between buckets, and how many buckets the window covers.
type TimeseriesWindow struct {
	// Period is the resolved period name.
	Period string
	// Trunc is the bucket width: "minute", "hour" or "day".
	Trunc string
	// Step is the distance between two bucket boundaries.
	Step time.Duration
	// Count is how many buckets the window covers.
	Count int
}

// timeseriesWindows are the period names the reference accepts, with the
// bucketing each one covers.
var timeseriesWindows = map[string]TimeseriesWindow{
	"1h":  {Period: "1h", Trunc: "minute", Step: time.Minute, Count: 60},
	"12h": {Period: "12h", Trunc: "hour", Step: time.Hour, Count: 12},
	"1d":  {Period: "1d", Trunc: "hour", Step: time.Hour, Count: 24},
	"7d":  {Period: "7d", Trunc: "day", Step: 24 * time.Hour, Count: 7},
	"30d": {Period: "30d", Trunc: "day", Step: 24 * time.Hour, Count: 30},
	"90d": {Period: "90d", Trunc: "day", Step: 24 * time.Hour, Count: 90},
}

// DefaultTimeseriesWindow is the period an unrecognized name resolves to, which
// is what the reference does rather than rejecting the request.
var DefaultTimeseriesWindow = timeseriesWindows["7d"]

// ResolveTimeseriesWindow maps a period name onto its bucketing.
func ResolveTimeseriesWindow(period string) TimeseriesWindow {
	if window, ok := timeseriesWindows[period]; ok {
		return window
	}
	return DefaultTimeseriesWindow
}

// TimeseriesTimeFields are the axes the series can be bucketed by; anything else
// resolves to created_at, as the reference resolves it.
var TimeseriesTimeFields = []string{"created_at", "mentioned_at", "occurred_start"}

// ResolveTimeseriesTimeField maps a requested axis onto one the store can read.
func ResolveTimeseriesTimeField(field string) string {
	for _, known := range TimeseriesTimeFields {
		if known == field {
			return field
		}
	}
	return "created_at"
}

// truncateBucket floors a timestamp to a bucket width.
func truncateBucket(value time.Time, width string) time.Time {
	value = value.UTC()
	switch width {
	case "minute":
		return value.Truncate(time.Minute)
	case "hour":
		return value.Truncate(time.Hour)
	default:
		return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
	}
}
