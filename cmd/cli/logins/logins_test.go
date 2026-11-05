package logins

import (
	"math"
	"strings"
	"testing"

	"github.com/spf13/viper"

	"github.com/sil-org/idp-cli/cmd/cli/flags"
)

func setScanLimits(t *testing.T, segments int, maxRCU float64, maxRecords int) {
	t.Helper()
	t.Cleanup(viper.Reset)
	viper.Set(flags.ScanSegments, segments)
	viper.Set(flags.MaxRCU, maxRCU)
	viper.Set(flags.MaxRecords, maxRecords)
}

func TestScanLimits_AcceptsZeroAsUnlimited(t *testing.T) {
	setScanLimits(t, 1, 0, 0)
	s, err := scanLimits()
	if err != nil {
		t.Fatal(err)
	}
	if s.Segments != 1 || s.MaxRCU != 0 || s.MaxRecords != 0 {
		t.Errorf("got %+v", s)
	}
}

func TestScanLimits_RejectsOutOfRangeValues(t *testing.T) {
	tests := []struct {
		name       string
		segments   int
		maxRCU     float64
		maxRecords int
		wantFlag   string
	}{
		{"zero segments", 0, 500, 10, flags.ScanSegments},
		{"negative segments", -2, 500, 10, flags.ScanSegments},
		{"too many segments", maxScanSegments + 1, 500, 10, flags.ScanSegments},
		{"negative max-rcu", 4, -1, 10, flags.MaxRCU},
		{"NaN max-rcu", 4, math.NaN(), 10, flags.MaxRCU},
		{"negative max-records", 4, 500, -1, flags.MaxRecords},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setScanLimits(t, tt.segments, tt.maxRCU, tt.maxRecords)
			_, err := scanLimits()
			if err == nil || !strings.Contains(err.Error(), tt.wantFlag) {
				t.Errorf("err = %v, want an error naming %s", err, tt.wantFlag)
			}
		})
	}
}
