package loginlog

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// fakeDynamo serves each segment as pages (default 2) of one item each.
type fakeDynamo struct {
	mu         sync.Mutex
	inputs     []*dynamodb.ScanInput
	failOn     int32   // segment that returns an error, or -1
	pages      int     // pages per segment; 0 means 2
	rcuPerPage float64 // 0 means 1.5
}

func (f *fakeDynamo) Scan(ctx context.Context, in *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	f.mu.Lock()
	f.inputs = append(f.inputs, in)
	f.mu.Unlock()

	segment := aws.ToInt32(in.Segment)
	if segment == f.failOn {
		return nil, errors.New("boom")
	}

	page := 0
	if start, ok := in.ExclusiveStartKey["page"].(*types.AttributeValueMemberN); ok {
		fmt.Sscan(start.Value, &page)
		page++
	}
	pages := cmp.Or(f.pages, 2)
	rcu := cmp.Or(f.rcuPerPage, 1.5)
	item, err := attributevalue.MarshalMap(Entry{
		ID:   fmt.Sprintf("seg%d-page%d", segment, page),
		Time: "2026-10-01 00:00:00",
		SP:   "sp", IDP: "idp",
	})
	if err != nil {
		return nil, err
	}

	out := &dynamodb.ScanOutput{
		Items:            []map[string]types.AttributeValue{item},
		Count:            1,
		ScannedCount:     3,
		ConsumedCapacity: &types.ConsumedCapacity{CapacityUnits: aws.Float64(rcu)},
	}
	if page < pages-1 {
		out.LastEvaluatedKey = map[string]types.AttributeValue{"page": &types.AttributeValueMemberN{Value: fmt.Sprint(page)}}
	}
	return out, nil
}

func TestScan_RateLimitThrottlesAfterBurst(t *testing.T) {
	// Each page costs a full 128 RCU (the burst size). At 1280 RCU/s the
	// first page is covered by the initial burst; each later page waits
	// ~100ms. 4 pages => 3 waits, the last of which is skipped (no next
	// page to protect) => ~200ms.
	fake := &fakeDynamo{failOn: -1, pages: 4, rcuPerPage: 128}
	s := Scanner{Client: fake, Table: "log", Segments: 1, MaxRCU: 1280}

	start := time.Now()
	entries, err := s.Scan(t.Context(), Filter{}, nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatalf("got %d entries, want 4", len(entries))
	}
	if elapsed < 150*time.Millisecond {
		t.Errorf("scan took %v; expected the rate limit to slow it to ~200ms", elapsed)
	}

	// Unlimited: no waiting at all.
	s.MaxRCU = 0
	start = time.Now()
	if _, err := s.Scan(t.Context(), Filter{}, nil); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Errorf("unlimited scan took %v", elapsed)
	}
}

func TestScan_CanceledContextStopsScan(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	fake := &fakeDynamo{failOn: -1, pages: 4, rcuPerPage: 128}
	// A very low limit would block for a long time if cancellation were ignored.
	s := Scanner{Client: fake, Table: "log", Segments: 1, MaxRCU: 1}

	_, err := s.Scan(ctx, Filter{}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestScan_ParallelSegmentsAndPagination(t *testing.T) {
	fake := &fakeDynamo{failOn: -1}
	s := Scanner{Client: fake, Table: "log", Segments: 3, MaxRCU: 1000}
	progress := &Progress{}

	entries, err := s.Scan(t.Context(), Filter{SP: "sp"}, progress)
	if err != nil {
		t.Fatal(err)
	}

	ids := make([]string, len(entries))
	for i, e := range entries {
		ids[i] = e.ID
	}
	slices.Sort(ids)
	want := []string{"seg0-page0", "seg0-page1", "seg1-page0", "seg1-page1", "seg2-page0", "seg2-page1"}
	if !slices.Equal(ids, want) {
		t.Errorf("ids = %v, want %v", ids, want)
	}

	if got := progress.Scanned.Load(); got != 18 {
		t.Errorf("Scanned = %d, want 18", got)
	}
	if got := progress.Matched.Load(); got != 6 {
		t.Errorf("Matched = %d, want 6", got)
	}
	if got := progress.ConsumedRCU(); got != 9 {
		t.Errorf("ConsumedRCU = %v, want 9", got)
	}

	for _, in := range fake.inputs {
		if aws.ToString(in.TableName) != "log" || aws.ToInt32(in.TotalSegments) != 3 {
			t.Errorf("unexpected input: table=%q totalSegments=%d", aws.ToString(in.TableName), aws.ToInt32(in.TotalSegments))
		}
		if aws.ToString(in.FilterExpression) != "#sp = :sp" {
			t.Errorf("FilterExpression = %q", aws.ToString(in.FilterExpression))
		}
		if in.ReturnConsumedCapacity != types.ReturnConsumedCapacityTotal {
			t.Errorf("ReturnConsumedCapacity not requested")
		}
	}
}

func TestScan_SingleSegmentOmitsSegmentParams(t *testing.T) {
	fake := &fakeDynamo{failOn: -1}
	s := Scanner{Client: fake, Table: "log", Segments: 0}

	if _, err := s.Scan(t.Context(), Filter{}, nil); err != nil {
		t.Fatal(err)
	}
	for _, in := range fake.inputs {
		if in.Segment != nil || in.TotalSegments != nil {
			t.Errorf("single-segment scan should not set Segment/TotalSegments")
		}
		if in.FilterExpression != nil {
			t.Errorf("empty filter should not set FilterExpression")
		}
	}
}

func TestScan_SegmentErrorFailsWholeScan(t *testing.T) {
	fake := &fakeDynamo{failOn: 1}
	s := Scanner{Client: fake, Table: "log", Segments: 3}

	_, err := s.Scan(t.Context(), Filter{}, nil)
	if err == nil || !strings.Contains(err.Error(), "segment 1") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("expected segment 1 error, got %v", err)
	}
}

func TestScan_RequiresTable(t *testing.T) {
	if _, err := (Scanner{Client: &fakeDynamo{}}).Scan(t.Context(), Filter{}, nil); err == nil {
		t.Errorf("expected an error without a table")
	}
}

func TestScan_MaxRecordsStopsWithTypedError(t *testing.T) {
	// 3 segments x 4 pages x 1 item = 12 matches, over the limit of 5.
	fake := &fakeDynamo{failOn: -1, pages: 4}
	s := Scanner{Client: fake, Table: "log", Segments: 3, MaxRecords: 5}

	_, err := s.Scan(t.Context(), Filter{}, nil)
	tooMany, ok := errors.AsType[*TooManyRecordsError](err)
	if !ok {
		t.Fatalf("expected a *TooManyRecordsError, got %v", err)
	}
	if tooMany.Limit != 5 {
		t.Errorf("Limit = %d, want 5", tooMany.Limit)
	}
	if !strings.HasPrefix(err.Error(), "more than 5 logins match") {
		t.Errorf("unhelpful message: %v", err)
	}
}

func TestScan_MaxRecordsNotExceeded(t *testing.T) {
	fake := &fakeDynamo{failOn: -1, pages: 2}
	s := Scanner{Client: fake, Table: "log", Segments: 3, MaxRecords: 6} // exactly 6 matches

	entries, err := s.Scan(t.Context(), Filter{}, nil)
	if err != nil {
		t.Fatalf("6 matches with a limit of 6 should succeed: %v", err)
	}
	if len(entries) != 6 {
		t.Errorf("got %d entries, want 6", len(entries))
	}
}
