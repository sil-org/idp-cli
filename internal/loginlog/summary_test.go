package loginlog

import (
	"slices"
	"testing"
)

var sample = []Entry{
	{ID: "1", Time: "2026-10-01 09:00:00", SP: "sp-a", IDP: "idp-1", Hub: "hub-sil", EmployeeNumber: "100"},
	{ID: "2", Time: "2026-10-01 10:00:00", SP: "sp-a", IDP: "idp-1", Hub: "hub-sil", EmployeeNumber: "100"},
	{ID: "3", Time: "2026-10-02 08:00:00", SP: "sp-a", IDP: "idp-1", Hub: "hub-wyc", EmployeeNumber: "200"},
	{ID: "4", Time: "2026-10-02 11:00:00", SP: "sp-b", IDP: "idp-2", CN: "Jane Doe"},
}

func TestSummarize_BySP(t *testing.T) {
	got := Summarize(sample, BySP)
	want := []Group{
		{Key: "sp-a", Logins: 3, Users: 2, First: "2026-10-01 09:00:00", Last: "2026-10-02 08:00:00"},
		{Key: "sp-b", Logins: 1, Users: 1, First: "2026-10-02 11:00:00", Last: "2026-10-02 11:00:00"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}

func TestSummarize_ByHubIncludesDirect(t *testing.T) {
	got := Summarize(sample, ByHub)
	keys := make([]string, len(got))
	for i, g := range got {
		keys[i] = g.Key
	}
	// hub-sil has 2 logins; the 1-login groups tie and sort by key.
	want := []string{"hub-sil", "(direct)", "hub-wyc"}
	if !slices.Equal(keys, want) {
		t.Errorf("keys = %v, want %v", keys, want)
	}
}

func TestSummarize_ByDayIsChronological(t *testing.T) {
	got := Summarize(sample, ByDay)
	if len(got) != 2 || got[0].Key != "2026-10-01" || got[1].Key != "2026-10-02" {
		t.Fatalf("unexpected days: %+v", got)
	}
	if got[0].Logins != 2 || got[0].Users != 1 {
		t.Errorf("day 1 = %+v, want 2 logins by 1 user", got[0])
	}
}

func TestSummarize_ByUserFallsBackToCN(t *testing.T) {
	got := Summarize(sample, ByUser)
	if !slices.ContainsFunc(got, func(g Group) bool { return g.Key == "Jane Doe" && g.Logins == 1 }) {
		t.Errorf("expected a CN-keyed group, got %+v", got)
	}
}

func TestTotal(t *testing.T) {
	got := Total(sample)
	want := Totals{Logins: 4, Users: 3, First: "2026-10-01 09:00:00", Last: "2026-10-02 11:00:00"}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if (Total(nil) != Totals{}) {
		t.Errorf("Total(nil) should be zero")
	}
}

func TestSortNewestFirst(t *testing.T) {
	entries := slices.Clone(sample)
	SortNewestFirst(entries)
	ids := make([]string, len(entries))
	for i, e := range entries {
		ids[i] = e.ID
	}
	if want := []string{"4", "3", "2", "1"}; !slices.Equal(ids, want) {
		t.Errorf("order = %v, want %v", ids, want)
	}
}

func TestParseGroupBy(t *testing.T) {
	if g, err := ParseGroupBy(" HUB "); err != nil || g != ByHub {
		t.Errorf("ParseGroupBy(HUB) = %q, %v", g, err)
	}
	if _, err := ParseGroupBy("country"); err == nil {
		t.Errorf("expected error for unknown grouping")
	}
}

func TestTop_KeepsTheLargestGroups(t *testing.T) {
	groups := Summarize(sample, BySP)
	got := Top(groups, BySP, 1)
	if len(got) != 1 || got[0].Key != "sp-a" {
		t.Errorf("Top 1 by SP = %+v, want just sp-a", got)
	}
	if got := Top(groups, BySP, 0); len(got) != len(groups) {
		t.Errorf("Top 0 should keep all %d groups, got %d", len(groups), len(got))
	}
}

func TestTop_ByDayPicksBusiestDaysInDateOrder(t *testing.T) {
	day := func(d string, n int) []Entry {
		entries := make([]Entry, n)
		for i := range entries {
			entries[i] = Entry{ID: d + string(rune('a'+i)), Time: d + " 09:00:00"}
		}
		return entries
	}
	var entries []Entry
	for _, d := range []struct {
		date   string
		logins int
	}{
		{"2026-10-01", 1}, {"2026-10-02", 5}, {"2026-10-03", 2}, {"2026-10-04", 4}, {"2026-10-05", 2},
	} {
		entries = append(entries, day(d.date, d.logins)...)
	}

	got := Top(Summarize(entries, ByDay), ByDay, 3)
	keys := make([]string, len(got))
	for i, g := range got {
		keys[i] = g.Key
	}
	// The 3 busiest are 10-02 (5), 10-04 (4), and a tie at 2 that goes to the
	// more recent 10-05 -- listed by date, not by count.
	if want := []string{"2026-10-02", "2026-10-04", "2026-10-05"}; !slices.Equal(keys, want) {
		t.Errorf("keys = %v, want %v", keys, want)
	}
}
