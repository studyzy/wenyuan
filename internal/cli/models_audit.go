package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
)

// auditEntryOut is one audit trail entry. duration_ms is derived
// server-side and omitted when the entry has no end.
type auditEntryOut struct {
	ID         string         `json:"id" yaml:"id"`
	Action     string         `json:"action" yaml:"action"`
	Transport  string         `json:"transport" yaml:"transport"`
	BankID     *string        `json:"bank_id" yaml:"bank_id"`
	StartedAt  *string        `json:"started_at" yaml:"started_at"`
	EndedAt    *string        `json:"ended_at" yaml:"ended_at"`
	DurationMS *int           `json:"duration_ms" yaml:"duration_ms"`
	Request    map[string]any `json:"request" yaml:"request"`
	Response   map[string]any `json:"response" yaml:"response"`
	Metadata   map[string]any `json:"metadata" yaml:"metadata"`
}

// auditListOut is the audit-log listing envelope.
type auditListOut struct {
	BankID string          `json:"bank_id" yaml:"bank_id"`
	Total  int             `json:"total" yaml:"total"`
	Limit  int             `json:"limit" yaml:"limit"`
	Offset int             `json:"offset" yaml:"offset"`
	Items  []auditEntryOut `json:"items" yaml:"items"`
}

// auditStatsBucket is one time bucket of the stats view: a count by action.
type auditStatsBucket struct {
	Time    string         `json:"time" yaml:"time"`
	Actions map[string]int `json:"actions" yaml:"actions"`
	Total   int            `json:"total" yaml:"total"`
}

// auditStatsOut is the stats response.
type auditStatsOut struct {
	BankID  string             `json:"bank_id" yaml:"bank_id"`
	Period  string             `json:"period" yaml:"period"`
	Trunc   string             `json:"trunc" yaml:"trunc"`
	Start   string             `json:"start" yaml:"start"`
	Buckets []auditStatsBucket `json:"buckets" yaml:"buckets"`
}

// RegisterAuditFamily wires the audit commands onto the registry. This is
// the one family whose CLI exposes filters and offset paging.
func RegisterAuditFamily(r *Registry) {
	audit := r.Sub("audit", "inspect the audit trail")

	list := audit.Sub("list", "list audit log entries")
	var lf auditListFlags
	list.Flags().StringVar(&lf.action, "action", "", "filter by action (e.g. recall, retain)")
	list.Flags().StringVar(&lf.transport, "transport", "", "filter by transport (e.g. http, mcp)")
	list.Flags().StringVar(&lf.startDate, "start-date", "", "window start (ISO 8601)")
	list.Flags().StringVar(&lf.endDate, "end-date", "", "window end (ISO 8601)")
	list.Flags().IntVar(&lf.limit, "limit", 0, "page size")
	list.Flags().IntVar(&lf.limit, "l", 0, "page size (short)")
	list.Flags().IntVar(&lf.offset, "offset", 0, "page offset")
	list.Flags().IntVar(&lf.offset, "s", 0, "page offset (short)")
	list.Action(func(ctx context.Context, d *Deps) error {
		if len(d.Args) != 1 {
			return NewUsageError("audit list requires exactly one <bank_id>")
		}
		return auditList(ctx, d, d.Args[0], lf)
	})

	stats := audit.Sub("stats", "bucketed audit counts")
	var sf auditStatsFlags
	stats.Flags().StringVar(&sf.action, "action", "", "narrow the counts to one action")
	stats.Flags().StringVar(&sf.period, "period", "", "window name (e.g. day, week, month)")
	stats.Action(func(ctx context.Context, d *Deps) error {
		if len(d.Args) != 1 {
			return NewUsageError("audit stats requires exactly one <bank_id>")
		}
		return auditStats(ctx, d, d.Args[0], sf)
	})
}

// auditListFlags holds the list flag values.
type auditListFlags struct {
	action    string
	transport string
	startDate string
	endDate   string
	limit     int
	offset    int
}

// auditStatsFlags holds the stats flag values.
type auditStatsFlags struct {
	action string
	period string
}

// auditQuery renders the non-empty filters as a query string, empty when no
// filter was passed.
func auditQuery(pairs [][2]string) string {
	values := url.Values{}
	for _, pair := range pairs {
		if pair[1] != "" {
			values.Set(pair[0], pair[1])
		}
	}
	if len(values) == 0 {
		return ""
	}
	return "?" + values.Encode()
}

func auditList(ctx context.Context, d *Deps, bank string, f auditListFlags) error {
	query := auditQuery([][2]string{
		{"action", f.action},
		{"transport", f.transport},
		{"start_date", f.startDate},
		{"end_date", f.endDate},
		{"limit", itoaPositive(f.limit)},
		{"offset", itoaPositive(f.offset)},
	})
	var out auditListOut
	path := fmt.Sprintf("%s/%s/audit-logs%s", batchDBase, bank, query)
	if err := d.Client.Do(ctx, "GET", path, nil, &out); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return d.Format.Render(d.UI.Stdout, out)
	}
	ui := d.UI
	ui.Infof("Audit logs: %s", bank)
	ui.Plain("  Showing: %d (%d total)", len(out.Items), out.Total)
	ui.Plain("")
	if len(out.Items) == 0 {
		ui.Infof("No audit log entries.")
		return nil
	}
	for _, e := range out.Items {
		started := "-"
		if e.StartedAt != nil {
			started = *e.StartedAt
		}
		duration := "-"
		if e.DurationMS != nil {
			duration = fmt.Sprintf("%dms", *e.DurationMS)
		}
		ui.Plain("  %s %s [%s] %s", started, e.Action, e.Transport, duration)
	}
	return nil
}

func auditStats(ctx context.Context, d *Deps, bank string, f auditStatsFlags) error {
	query := auditQuery([][2]string{{"action", f.action}, {"period", f.period}})
	var out auditStatsOut
	path := fmt.Sprintf("%s/%s/audit-logs/stats%s", batchDBase, bank, query)
	if err := d.Client.Do(ctx, "GET", path, nil, &out); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return d.Format.Render(d.UI.Stdout, out)
	}
	ui := d.UI
	ui.Infof("Audit stats: %s", bank)
	ui.Plain("  Period: %s", out.Period)
	ui.Plain("  Start: %s", out.Start)
	ui.Plain("  Bucket: %s", out.Trunc)
	if len(out.Buckets) == 0 {
		ui.Infof("No activity in this period.")
		return nil
	}
	ui.Plain("")
	for _, bucket := range out.Buckets {
		line, err := json.Marshal(bucket)
		if err != nil {
			return err
		}
		ui.Plain("%s", string(line))
	}
	return nil
}

// itoaPositive renders a positive integer, empty for zero and negatives so
// unset paging flags stay out of the query.
func itoaPositive(v int) string {
	if v <= 0 {
		return ""
	}
	return strconv.Itoa(v)
}
