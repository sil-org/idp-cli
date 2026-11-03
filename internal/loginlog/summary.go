package loginlog

import (
	"cmp"
	"fmt"
	"iter"
	"slices"
	"strings"
)

// GroupBy selects the dimension a Summary groups logins by.
type GroupBy string

const (
	BySP   GroupBy = "sp"
	ByHub  GroupBy = "hub"
	ByIDP  GroupBy = "idp"
	ByDay  GroupBy = "day"
	ByUser GroupBy = "user"
)

// GroupBys lists every supported grouping, in display order.
var GroupBys = []GroupBy{BySP, ByHub, ByIDP, ByDay, ByUser}

// ParseGroupBy validates a --by argument.
func ParseGroupBy(s string) (GroupBy, error) {
	g := GroupBy(strings.ToLower(strings.TrimSpace(s)))
	if slices.Contains(GroupBys, g) {
		return g, nil
	}
	names := make([]string, len(GroupBys))
	for i, g := range GroupBys {
		names[i] = string(g)
	}
	return "", fmt.Errorf("unknown grouping %q (choose one of: %s)", s, strings.Join(names, ", "))
}

// Key returns the group key for an entry.
func (g GroupBy) Key(e Entry) string {
	switch g {
	case BySP:
		return e.SP
	case ByHub:
		return e.HubOrDirect()
	case ByIDP:
		return e.IDP
	case ByDay:
		return e.Day()
	case ByUser:
		return e.User()
	default:
		return ""
	}
}

// Group is one row of a Summary.
type Group struct {
	Key    string `json:"key"`
	Logins int    `json:"logins"`
	Users  int    `json:"users"` // distinct users
	First  string `json:"first"` // earliest Time
	Last   string `json:"last"`  // latest Time
}

// Summarize groups entries by g. Groups are sorted by login count
// (descending), except ByDay, which is chronological.
func Summarize(entries []Entry, g GroupBy) []Group {
	return SummarizeSeq(slices.Values(entries), g)
}

// SummarizeSeq is Summarize over an iterator, so callers holding entries
// some other way (e.g. as indices into a larger slice) needn't copy them.
func SummarizeSeq(entries iter.Seq[Entry], g GroupBy) []Group {
	type acc struct {
		Group
		users map[string]struct{}
	}
	groups := map[string]*acc{}

	for e := range entries {
		key := g.Key(e)
		a, ok := groups[key]
		if !ok {
			a = &acc{Group: Group{Key: key, First: e.Time, Last: e.Time}, users: map[string]struct{}{}}
			groups[key] = a
		}
		a.Logins++
		a.users[e.User()] = struct{}{}
		a.First = min(a.First, e.Time)
		a.Last = max(a.Last, e.Time)
	}

	result := make([]Group, 0, len(groups))
	for _, a := range groups {
		a.Users = len(a.users)
		result = append(result, a.Group)
	}

	slices.SortFunc(result, func(a, b Group) int {
		if g == ByDay {
			return cmp.Compare(a.Key, b.Key)
		}
		return cmp.Or(cmp.Compare(b.Logins, a.Logins), cmp.Compare(a.Key, b.Key))
	})
	return result
}

// Totals summarizes a whole result set.
type Totals struct {
	Logins int    `json:"logins"`
	Users  int    `json:"users"`
	First  string `json:"first,omitempty"`
	Last   string `json:"last,omitempty"`
}

// Total computes overall totals for entries.
func Total(entries []Entry) Totals {
	return TotalSeq(slices.Values(entries))
}

// TotalSeq is Total over an iterator.
func TotalSeq(entries iter.Seq[Entry]) Totals {
	all := SummarizeSeq(entries, GroupBy("")) // every entry shares the "" key
	if len(all) == 0 {
		return Totals{}
	}
	return Totals{Logins: all[0].Logins, Users: all[0].Users, First: all[0].First, Last: all[0].Last}
}

// SortNewestFirst orders entries by Time, newest first.
func SortNewestFirst(entries []Entry) {
	slices.SortFunc(entries, func(a, b Entry) int {
		return cmp.Or(cmp.Compare(b.Time, a.Time), cmp.Compare(a.ID, b.ID))
	})
}
