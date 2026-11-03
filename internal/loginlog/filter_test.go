package loginlog

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func TestParseTimeArg(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		in       string
		endOfDay bool
		want     time.Time
	}{
		{"", false, time.Time{}},
		{"24h", false, now.Add(-24 * time.Hour)},
		{"90m", false, now.Add(-90 * time.Minute)},
		{"7d", false, now.Add(-7 * 24 * time.Hour)},
		{"2w", false, now.Add(-14 * 24 * time.Hour)},
		{"2026-10-01", false, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)},
		{"2026-10-01", true, time.Date(2026, 10, 1, 23, 59, 59, 0, time.UTC)},
		{"2026-10-01 13:45", false, time.Date(2026, 10, 1, 13, 45, 0, 0, time.UTC)},
		{"2026-10-01 13:45:30", false, time.Date(2026, 10, 1, 13, 45, 30, 0, time.UTC)},
		{"2026-10-01T13:45:00-05:00", false, time.Date(2026, 10, 1, 18, 45, 0, 0, time.UTC)},
	}
	for _, tt := range tests {
		got, err := ParseTimeArg(tt.in, now, tt.endOfDay)
		if err != nil {
			t.Errorf("ParseTimeArg(%q): unexpected error %v", tt.in, err)
			continue
		}
		if !got.Equal(tt.want) {
			t.Errorf("ParseTimeArg(%q, endOfDay=%v) = %v, want %v", tt.in, tt.endOfDay, got, tt.want)
		}
	}

	for _, bad := range []string{"yesterday", "-5d", "2026-13-01", "7x"} {
		if _, err := ParseTimeArg(bad, now, false); err == nil {
			t.Errorf("ParseTimeArg(%q): expected an error", bad)
		}
	}
}

func TestFilterExpression_Empty(t *testing.T) {
	expr, names, values := Filter{}.expression()
	if expr != "" || names != nil || values != nil {
		t.Errorf("empty filter produced %q %v %v", expr, names, values)
	}
}

func TestFilterExpression_AllFields(t *testing.T) {
	f := Filter{
		Since: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 10, 31, 23, 59, 59, 0, time.UTC),
		IDP:   "idp.example",
		SP:    "sp.example",
		Hub:   "hub.example",
		User:  "10001",
	}
	expr, names, values := f.expression()

	want := "#t >= :since AND #t <= :until AND #idp = :idp AND #sp = :sp AND #hub = :hub" +
		" AND (#emp = :user OR #eppn = :user OR #cn = :user)"
	if expr != want {
		t.Errorf("expression:\n got %q\nwant %q", expr, want)
	}
	if names["#t"] != "Time" || names["#hub"] != "Hub" || names["#emp"] != "EmployeeNumber" {
		t.Errorf("unexpected names: %v", names)
	}
	assertS(t, values, ":since", "2026-10-01 00:00:00")
	assertS(t, values, ":until", "2026-10-31 23:59:59")
	assertS(t, values, ":hub", "hub.example")
	assertS(t, values, ":user", "10001")
}

func TestFilterExpression_DirectHub(t *testing.T) {
	expr, names, values := Filter{Hub: DirectHub}.expression()
	if expr != "attribute_not_exists(#hub)" {
		t.Errorf("got %q", expr)
	}
	if names["#hub"] != "Hub" {
		t.Errorf("unexpected names: %v", names)
	}
	// Must be nil, not an empty map: DynamoDB rejects empty ExpressionAttributeValues.
	if values != nil {
		t.Errorf("expected nil values, got %#v", values)
	}
}

func assertS(t *testing.T, values map[string]types.AttributeValue, key, want string) {
	t.Helper()
	s, ok := values[key].(*types.AttributeValueMemberS)
	if !ok {
		t.Errorf("value %s missing or not a string: %#v", key, values[key])
		return
	}
	if s.Value != want {
		t.Errorf("value %s = %q, want %q", key, s.Value, want)
	}
}

func TestFilterMatches(t *testing.T) {
	e := Entry{Time: "2026-10-15 12:00:00", IDP: "idp", SP: "sp", Hub: "hub", EmployeeNumber: "100", CN: "jdoe"}
	direct := Entry{Time: "2026-10-15 12:00:00", IDP: "idp", SP: "sp"}
	day := func(d int) time.Time { return time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC) }

	tests := []struct {
		name  string
		f     Filter
		entry Entry
		want  bool
	}{
		{"empty filter matches", Filter{}, e, true},
		{"since before", Filter{Since: day(15)}, e, true},
		{"since after", Filter{Since: day(16)}, e, false},
		{"since exactly equal is inclusive", Filter{Since: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}, e, true},
		{"until after", Filter{Until: day(16)}, e, true},
		{"until before", Filter{Until: day(15)}, e, false},
		{"until exactly equal is inclusive", Filter{Until: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}, e, true},
		{"idp match", Filter{IDP: "idp"}, e, true},
		{"idp mismatch", Filter{IDP: "other"}, e, false},
		{"sp mismatch", Filter{SP: "other"}, e, false},
		{"hub match", Filter{Hub: "hub"}, e, true},
		{"hub mismatch", Filter{Hub: "other"}, e, false},
		{"direct excludes hub logins", Filter{Hub: DirectHub}, e, false},
		{"direct includes direct logins", Filter{Hub: DirectHub}, direct, true},
		{"named hub excludes direct logins", Filter{Hub: "hub"}, direct, false},
		{"user by employee number", Filter{User: "100"}, e, true},
		{"user by cn", Filter{User: "jdoe"}, e, true},
		{"user mismatch", Filter{User: "200"}, e, false},
		{"all fields", Filter{Since: day(1), Until: day(31), IDP: "idp", SP: "sp", Hub: "hub", User: "100"}, e, true},
	}
	for _, tt := range tests {
		if got := tt.f.Matches(tt.entry); got != tt.want {
			t.Errorf("%s: Matches = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestFilterCoversTimeRange(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC) }

	tests := []struct {
		name          string
		loaded, other Filter
		want          bool
	}{
		{"everything loaded covers anything", Filter{}, Filter{Since: day(5)}, true},
		{"narrower since is covered", Filter{Since: day(1)}, Filter{Since: day(5)}, true},
		{"same since is covered", Filter{Since: day(5)}, Filter{Since: day(5)}, true},
		{"earlier since is not covered", Filter{Since: day(5)}, Filter{Since: day(1)}, false},
		{"no since is not covered by a since", Filter{Since: day(5)}, Filter{}, false},
		{"open until covers a later until", Filter{Since: day(1)}, Filter{Since: day(1), Until: day(20)}, true},
		{"narrower until is covered", Filter{Until: day(20)}, Filter{Until: day(10)}, true},
		{"later until is not covered", Filter{Until: day(10)}, Filter{Until: day(20)}, false},
		{"open until is not covered by a bounded until", Filter{Until: day(10)}, Filter{}, false},
		{"other fields don't matter", Filter{Since: day(1)}, Filter{Since: day(2), SP: "sp", Hub: DirectHub}, true},
	}
	for _, tt := range tests {
		if got := tt.loaded.CoversTimeRange(tt.other); got != tt.want {
			t.Errorf("%s: CoversTimeRange = %v, want %v", tt.name, got, tt.want)
		}
	}
}
