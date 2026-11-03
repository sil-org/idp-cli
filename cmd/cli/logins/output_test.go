package logins

import (
	"strings"
	"testing"
	"time"

	"github.com/sil-org/idp-cli/internal/loginlog"
)

func TestCommas(t *testing.T) {
	for in, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567", -45000: "-45,000"} {
		if got := commas(in); got != want {
			t.Errorf("commas(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestWriteEntries_CSV(t *testing.T) {
	var b strings.Builder
	entries := []loginlog.Entry{{Time: "2026-10-01 09:00:00", EmployeeNumber: "100", CN: "Doe, Jane", SP: "sp", IDP: "idp"}}
	if err := writeEntries(&b, formatCSV, entries); err != nil {
		t.Fatal(err)
	}
	want := "Time (UTC),User,CN,SP,Hub,IdP\n2026-10-01 09:00:00,100,\"Doe, Jane\",sp,(direct),idp\n"
	if b.String() != want {
		t.Errorf("got %q\nwant %q", b.String(), want)
	}
}

func TestWriteGroups_JSON(t *testing.T) {
	var b strings.Builder
	groups := []loginlog.Group{{Key: "hub", Logins: 2, Users: 1}}
	if err := writeGroups(&b, formatJSON, loginlog.ByHub, groups, 1, loginlog.Totals{Logins: 2, Users: 1}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"by": "hub"`, `"logins": 2`, `"key": "hub"`} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("JSON missing %s:\n%s", want, b.String())
		}
	}
}

func TestFilterFlags_RejectsInvertedRange(t *testing.T) {
	ff := filterFlags{since: "2026-10-02", until: "2026-10-01"}
	if _, err := ff.filter(time.Now()); err == nil {
		t.Errorf("expected an error when --until is before --since")
	}
}

func TestWriteGroups_TableFooterCountsAllGroups(t *testing.T) {
	var b strings.Builder
	groups := []loginlog.Group{{Key: "a", Logins: 5, Users: 1}}
	if err := writeGroups(&b, formatTable, loginlog.ByUser, groups, 395, loginlog.Totals{Logins: 9, Users: 395}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "in 395 groups") {
		t.Errorf("footer should count all groups, not just the shown ones:\n%s", b.String())
	}
}
