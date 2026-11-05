package loginlog

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// Filter narrows a scan. Zero-valued fields don't filter. All comparisons are
// applied server-side as a FilterExpression -- which reduces data transferred,
// but NOT read capacity consumed: a Scan always reads the whole table.
type Filter struct {
	Since time.Time // inclusive
	Until time.Time // inclusive
	IDP   string
	SP    string
	Hub   string // "direct" matches logins with no Hub
	User  string // matches EmployeeNumber, EduPersonPrincipalName, or CN exactly
}

// DirectHub is the Filter.Hub value that selects logins without a Hub.
const DirectHub = "direct"

// TimeRange returns just the Since/Until part of the filter.
func (f Filter) TimeRange() Filter {
	return Filter{Since: f.Since, Until: f.Until}
}

// Matches reports whether e passes the filter, applying the same rules as
// the scan's server-side FilterExpression, so results can be re-filtered in
// memory without another scan.
func (f Filter) Matches(e Entry) bool {
	if !f.Since.IsZero() && e.Time < f.Since.UTC().Format(TimeLayout) {
		return false
	}
	if !f.Until.IsZero() && e.Time > f.Until.UTC().Format(TimeLayout) {
		return false
	}
	if f.IDP != "" && e.IDP != f.IDP {
		return false
	}
	if f.SP != "" && e.SP != f.SP {
		return false
	}
	switch f.Hub {
	case "":
	case DirectHub:
		if e.Hub != "" {
			return false
		}
	default:
		if e.Hub != f.Hub {
			return false
		}
	}
	if f.User != "" && e.EmployeeNumber != f.User && e.EduPersonPrincipalName != f.User && e.CN != f.User {
		return false
	}
	return true
}

// CoversTimeRange reports whether data scanned with f's time range includes
// everything in other's time range, so other can be applied in memory
// instead of rescanning. An open-ended Until (now, at scan time) is treated
// as covering any later Until: logins since the scan are simply not loaded
// until the next rescan.
func (f Filter) CoversTimeRange(other Filter) bool {
	if !f.Since.IsZero() && (other.Since.IsZero() || other.Since.Before(f.Since)) {
		return false
	}
	if !f.Until.IsZero() && (other.Until.IsZero() || other.Until.After(f.Until)) {
		return false
	}
	return true
}

// expression builds the DynamoDB FilterExpression and its placeholders.
// It returns an empty expression when the filter matches everything.
func (f Filter) expression() (expr string, names map[string]string, values map[string]types.AttributeValue) {
	var clauses []string
	names = map[string]string{}
	values = map[string]types.AttributeValue{}

	str := func(placeholder, v string) string {
		values[placeholder] = &types.AttributeValueMemberS{Value: v}
		return placeholder
	}
	name := func(placeholder, attr string) string {
		names[placeholder] = attr
		return placeholder
	}

	// "Time" is a DynamoDB reserved word, so every attribute goes through a
	// name placeholder for consistency.
	if !f.Since.IsZero() {
		clauses = append(clauses, name("#t", "Time")+" >= "+str(":since", f.Since.UTC().Format(TimeLayout)))
	}
	if !f.Until.IsZero() {
		clauses = append(clauses, name("#t", "Time")+" <= "+str(":until", f.Until.UTC().Format(TimeLayout)))
	}
	if f.IDP != "" {
		clauses = append(clauses, name("#idp", "IDP")+" = "+str(":idp", f.IDP))
	}
	if f.SP != "" {
		clauses = append(clauses, name("#sp", "SP")+" = "+str(":sp", f.SP))
	}
	switch f.Hub {
	case "":
	case DirectHub:
		clauses = append(clauses, "attribute_not_exists("+name("#hub", "Hub")+")")
	default:
		clauses = append(clauses, name("#hub", "Hub")+" = "+str(":hub", f.Hub))
	}
	if f.User != "" {
		u := str(":user", f.User)
		clauses = append(clauses, fmt.Sprintf("(%s = %s OR %s = %s OR %s = %s)",
			name("#emp", "EmployeeNumber"), u,
			name("#eppn", "EduPersonPrincipalName"), u,
			name("#cn", "CN"), u,
		))
	}

	if len(clauses) == 0 {
		return "", nil, nil
	}
	// DynamoDB rejects an empty (as opposed to absent) placeholder map, e.g.
	// for Hub=direct, whose attribute_not_exists() clause takes no values.
	if len(values) == 0 {
		values = nil
	}
	return strings.Join(clauses, " AND "), names, values
}

// ParseTimeArg parses a --since/--until style argument, relative to now.
// Accepted forms (all absolute forms are UTC, matching the stored Time):
//
//	30m, 24h, 7d, 2w   -- that long before now
//	2026-10-01         -- a date
//	2026-10-01 13:45   -- date and time (seconds optional)
//	2026-10-01T13:45:00Z -- RFC 3339
//
// When inclusiveEnd is set (for --until), a value without seconds is rounded
// up to the last second of the day or minute it names, so that whole day or
// minute is included.
func ParseTimeArg(s string, now time.Time, inclusiveEnd bool) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}

	if d, ok := parseRelative(s); ok {
		return now.Add(-d), nil
	}

	layouts := []struct {
		layout string
		unit   time.Duration // the precision of the layout, for inclusiveEnd
	}{
		{time.DateOnly, 24 * time.Hour},
		{time.DateTime, time.Second},
		{"2006-01-02 15:04", time.Minute},
		{time.RFC3339, time.Second},
		{"2006-01-02T15:04", time.Minute},
	}
	for _, l := range layouts {
		if t, err := time.Parse(l.layout, s); err == nil {
			if inclusiveEnd {
				t = t.Add(l.unit - time.Second)
			}
			return t.UTC(), nil
		}
	}

	return time.Time{}, fmt.Errorf("unrecognized time %q (try 24h, 7d, 2026-10-01, or \"2026-10-01 13:45\")", s)
}

func parseRelative(s string) (time.Duration, bool) {
	units := map[string]time.Duration{"d": 24 * time.Hour, "w": 7 * 24 * time.Hour}
	for suffix, unit := range units {
		if numStr, ok := strings.CutSuffix(s, suffix); ok {
			n, err := strconv.Atoi(numStr)
			if err != nil || n < 0 {
				return 0, false
			}
			return time.Duration(n) * unit, true
		}
	}

	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, false
	}
	return d, true
}
