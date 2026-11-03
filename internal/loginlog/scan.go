package loginlog

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"golang.org/x/time/rate"
)

// maxPageRCU is the most read capacity one Scan page can consume: a page is
// at most 1 MB, and an eventually consistent read costs 0.5 RCU per 4 KB.
const maxPageRCU = 128

// ScanAPI is the subset of the DynamoDB client the Scanner uses.
type ScanAPI interface {
	Scan(ctx context.Context, params *dynamodb.ScanInput, optFns ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error)
}

// Scanner reads the login log with a parallel, rate-limited Scan.
type Scanner struct {
	Client ScanAPI
	Table  string

	// Segments is the number of parallel scan workers (minimum 1).
	Segments int

	// MaxRCU caps read capacity units consumed per second across all
	// segments, so a scan doesn't starve the IdPs' PutItem calls on a
	// provisioned-capacity table. Zero or less means unlimited.
	MaxRCU float64

	// MaxRecords stops the scan with a *TooManyRecordsError once more than
	// this many entries have matched, rather than letting a too-wide query
	// exhaust memory (every match is held in memory). Zero or less means
	// unlimited.
	MaxRecords int
}

// TooManyRecordsError reports that a scan matched more than Scanner.MaxRecords.
type TooManyRecordsError struct {
	Limit int
}

func (e *TooManyRecordsError) Error() string {
	return fmt.Sprintf("more than %d logins match; narrow the time range (or raise --max-records)", e.Limit)
}

// Progress is updated live during a scan and is safe to read concurrently.
type Progress struct {
	Scanned  atomic.Int64 // items read
	Matched  atomic.Int64 // items that passed the filter
	milliRCU atomic.Int64
}

// ConsumedRCU is the read capacity consumed so far.
func (p *Progress) ConsumedRCU() float64 {
	return float64(p.milliRCU.Load()) / 1000
}

// Scan reads every matching entry. progress may be nil.
func (s Scanner) Scan(ctx context.Context, f Filter, progress *Progress) ([]Entry, error) {
	if s.Table == "" {
		return nil, errors.New("no log table specified")
	}
	if progress == nil {
		progress = &Progress{}
	}
	segments := max(s.Segments, 1)

	// The burst is one page's maximum cost -- not a second's worth of
	// MaxRCU -- so throttling applies from the first pages instead of
	// letting an initial spike through.
	var limiter *rate.Limiter
	if s.MaxRCU > 0 {
		limiter = rate.NewLimiter(rate.Limit(s.MaxRCU), maxPageRCU)
	}

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	var (
		mu      sync.Mutex
		entries []Entry
		wg      sync.WaitGroup
	)
	for segment := range segments {
		wg.Go(func() {
			found, err := s.scanSegment(ctx, f, int32(segment), int32(segments), limiter, progress)
			if _, ok := errors.AsType[*TooManyRecordsError](err); ok {
				cancel(err) // a whole-scan limit; which segment hit it is irrelevant
				return
			}
			if err != nil {
				cancel(fmt.Errorf("segment %d: %w", segment, err))
				return
			}
			mu.Lock()
			entries = append(entries, found...)
			mu.Unlock()
		})
	}
	wg.Wait()

	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	return entries, nil
}

func (s Scanner) scanSegment(
	ctx context.Context,
	f Filter,
	segment, totalSegments int32,
	limiter *rate.Limiter,
	progress *Progress,
) ([]Entry, error) {
	expr, names, values := f.expression()
	input := &dynamodb.ScanInput{
		TableName:              aws.String(s.Table),
		ReturnConsumedCapacity: types.ReturnConsumedCapacityTotal,
	}
	if totalSegments > 1 {
		input.Segment = aws.Int32(segment)
		input.TotalSegments = aws.Int32(totalSegments)
	}
	if expr != "" {
		input.FilterExpression = aws.String(expr)
		input.ExpressionAttributeNames = names
		input.ExpressionAttributeValues = values
	}

	var entries []Entry
	paginator := dynamodb.NewScanPaginator(s.Client, input)
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}

		var pageEntries []Entry
		if err := attributevalue.UnmarshalListOfMaps(page.Items, &pageEntries); err != nil {
			return nil, fmt.Errorf("decoding items: %w", err)
		}
		entries = append(entries, pageEntries...)

		progress.Scanned.Add(int64(page.ScannedCount))
		matched := progress.Matched.Add(int64(page.Count))
		if s.MaxRecords > 0 && matched > int64(s.MaxRecords) {
			return nil, &TooManyRecordsError{Limit: s.MaxRecords}
		}

		consumed := 0.0
		if page.ConsumedCapacity != nil && page.ConsumedCapacity.CapacityUnits != nil {
			consumed = *page.ConsumedCapacity.CapacityUnits
		}
		progress.milliRCU.Add(int64(consumed * 1000))

		// Pay for this page before requesting the next one.
		if limiter != nil && consumed > 0 && paginator.HasMorePages() {
			n := min(int(math.Ceil(consumed)), limiter.Burst())
			if err := limiter.WaitN(ctx, n); err != nil {
				return nil, err
			}
		}
	}
	return entries, nil
}
