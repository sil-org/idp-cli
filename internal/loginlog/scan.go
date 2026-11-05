package loginlog

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

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
// It only reports on a scan; the scan's own limits don't depend on it, so a
// Progress reused across scans just keeps accumulating.
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
		matched atomic.Int64 // this scan's matches, for MaxRecords
	)
	for segment := range segments {
		wg.Go(func() {
			found, err := s.scanSegment(ctx, f, int32(segment), int32(segments), limiter, &matched, progress)
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
	matched *atomic.Int64,
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
	budget := rcuBudget{limiter: limiter}
	// A page's cost is only known after it's read, so reserve an estimate
	// before each request -- a full page at first, then the previous page's
	// cost -- and charge any shortfall afterwards. Every page is paid for
	// before (or as) it's read, so MaxRCU holds across all segments from the
	// very first request.
	estimate := float64(maxPageRCU)
	paginator := dynamodb.NewScanPaginator(s.Client, input)
	for paginator.HasMorePages() {
		if err := budget.wait(ctx, estimate); err != nil {
			return nil, err
		}
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
		progress.Matched.Add(int64(page.Count))
		if total := matched.Add(int64(page.Count)); s.MaxRecords > 0 && total > int64(s.MaxRecords) {
			return nil, &TooManyRecordsError{Limit: s.MaxRecords}
		}

		consumed := 0.0
		if page.ConsumedCapacity != nil && page.ConsumedCapacity.CapacityUnits != nil {
			consumed = *page.ConsumedCapacity.CapacityUnits
		}
		progress.milliRCU.Add(int64(consumed * 1000))

		budget.charge(consumed - estimate)
		estimate = consumed
	}
	return entries, nil
}

// rcuBudget applies a shared rate limit (in RCU per second) to scan requests.
// A nil limiter means unlimited.
type rcuBudget struct {
	limiter *rate.Limiter
}

// wait blocks until rcu can be spent.
func (b rcuBudget) wait(ctx context.Context, rcu float64) error {
	if b.limiter == nil || rcu <= 0 {
		return nil
	}
	return b.limiter.WaitN(ctx, b.tokens(rcu))
}

// charge spends rcu without waiting, putting the shared budget into debt if
// need be, so that the next request -- from any segment -- waits it off.
// Used for a page that cost more than was reserved for it.
func (b rcuBudget) charge(rcu float64) {
	if b.limiter == nil || rcu <= 0 {
		return
	}
	b.limiter.ReserveN(time.Now(), b.tokens(rcu))
}

func (b rcuBudget) tokens(rcu float64) int {
	return min(int(math.Ceil(rcu)), b.limiter.Burst()) // a page never costs more than the burst
}
