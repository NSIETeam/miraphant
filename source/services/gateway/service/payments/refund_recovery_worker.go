package payments

import (
	"context"
	"errors"
	"time"

	"github.com/songquanpeng/one-api/common/logger"
	"github.com/songquanpeng/one-api/model"
)

const (
	defaultRefundRecoveryInterval = 30 * time.Second
	defaultRefundRecoveryBatch    = 50
	defaultRefundProviderCalls    = 5
	maxRefundRecoveryBatch        = 500
)

type RefundRecoveryOptions struct {
	Enabled          bool
	Interval         time.Duration
	BatchSize        int
	MaxProviderCalls int
}

type RefundRecoveryReport struct {
	RefundScanned    int
	ProviderCalls    int
	RefundFailed     int
	RefundSkipped    int
	InboxScanned     int
	InboxProcessed   int
	InboxQuarantined int
	InboxFailed      int
}

type RefundRecoveryWorker struct {
	enabled bool
	cancel  context.CancelFunc
	done    chan struct{}
	opts    RefundRecoveryOptions
}

func NormalizeRefundRecoveryOptions(options RefundRecoveryOptions) RefundRecoveryOptions {
	if options.Interval <= 0 {
		options.Interval = defaultRefundRecoveryInterval
	}
	if options.BatchSize <= 0 || options.BatchSize > maxRefundRecoveryBatch {
		options.BatchSize = defaultRefundRecoveryBatch
	}
	if options.MaxProviderCalls <= 0 || options.MaxProviderCalls > options.BatchSize {
		options.MaxProviderCalls = defaultRefundProviderCalls
		if options.MaxProviderCalls > options.BatchSize {
			options.MaxProviderCalls = options.BatchSize
		}
	}
	return options
}

// StartRefundRecoveryWorker starts no goroutine when disabled. The worker
// uses persistent keyset cursors and runs batches serially; Stop cancels any
// provider request and waits before callers close the database.
func StartRefundRecoveryWorker(parent context.Context, options RefundRecoveryOptions) (*RefundRecoveryWorker, error) {
	options = NormalizeRefundRecoveryOptions(options)
	worker := &RefundRecoveryWorker{opts: options, done: make(chan struct{})}
	if !options.Enabled {
		close(worker.done)
		return worker, nil
	}
	if parent == nil {
		return nil, errors.New("refund recovery worker requires a context")
	}
	ctx, cancel := context.WithCancel(parent)
	worker.cancel, worker.enabled = cancel, true
	go worker.run(ctx)
	return worker, nil
}

func (w *RefundRecoveryWorker) Enabled() bool { return w != nil && w.enabled }

func (w *RefundRecoveryWorker) Stop() {
	if w == nil {
		return
	}
	if w.cancel != nil {
		w.cancel()
	}
	<-w.done
}

func (w *RefundRecoveryWorker) run(ctx context.Context) {
	defer close(w.done)
	ticker := time.NewTicker(w.opts.Interval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		report, err := RunRefundRecoveryBatch(ctx, w.opts)
		if err != nil && ctx.Err() == nil {
			logger.SysLog("refund recovery batch failed")
		} else if report.ProviderCalls > 0 || report.InboxProcessed > 0 || report.InboxQuarantined > 0 {
			logger.SysLogf("refund recovery batch completed; provider_calls=%d inbox_processed=%d inbox_quarantined=%d", report.ProviderCalls, report.InboxProcessed, report.InboxQuarantined)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func RunRefundRecoveryBatch(ctx context.Context, options RefundRecoveryOptions) (RefundRecoveryReport, error) {
	options = NormalizeRefundRecoveryOptions(options)
	var report RefundRecoveryReport
	if !options.Enabled {
		return report, nil
	}
	if ctx == nil {
		return report, errors.New("refund recovery batch requires a context")
	}
	if err := model.RequirePointsSchema(); err != nil {
		return report, err
	}
	if err := runRefundInboxBatch(ctx, options, &report); err != nil {
		return report, err
	}
	return report, runRefundOperationBatch(ctx, options, &report)
}

func runRefundOperationBatch(ctx context.Context, options RefundRecoveryOptions, report *RefundRecoveryReport) error {
	cursor, err := model.GetPointRefundRecoveryCursor("refunds")
	if err != nil {
		return err
	}
	rows, hasMore, err := model.ListDuePointRefunds(cursor, time.Now().UTC().Unix(), options.BatchSize)
	if err != nil {
		return err
	}
	if len(rows) == 0 && cursor != 0 && !hasMore {
		if err := model.AdvancePointRefundRecoveryCursor("refunds", cursor, 0, true); err != nil {
			return err
		}
		cursor = 0
		rows, hasMore, err = model.ListDuePointRefunds(0, time.Now().UTC().Unix(), options.BatchSize)
		if err != nil {
			return err
		}
	}
	for i, refund := range rows {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if report.ProviderCalls >= options.MaxProviderCalls {
			break
		}
		report.RefundScanned++
		dispatch, dispatchErr := DispatchPointRefundOperation(ctx, refund.RefundKey)
		if dispatch.ProviderCalled {
			report.ProviderCalls++
		}
		if dispatchErr != nil {
			if errors.Is(dispatchErr, model.ErrPointRefundNotDue) || errors.Is(dispatchErr, model.ErrPointRefundInFlight) || errors.Is(dispatchErr, model.ErrPointRefundState) {
				report.RefundSkipped++
			} else {
				report.RefundFailed++
			}
		}
		expected := cursor
		cursor = refund.ID
		if err := model.AdvancePointRefundRecoveryCursor("refunds", expected, cursor, false); err != nil {
			return err
		}
		if i == len(rows)-1 && !hasMore {
			if err := model.AdvancePointRefundRecoveryCursor("refunds", cursor, 0, true); err != nil {
				return err
			}
		}
	}
	return nil
}

func runRefundInboxBatch(ctx context.Context, options RefundRecoveryOptions, report *RefundRecoveryReport) error {
	cursor, err := model.GetPointRefundRecoveryCursor("inbox")
	if err != nil {
		return err
	}
	rows, hasMore, err := model.ListReceivedPointRefundInbox(cursor, options.BatchSize)
	if err != nil {
		return err
	}
	if len(rows) == 0 && cursor != 0 && !hasMore {
		if err := model.AdvancePointRefundRecoveryCursor("inbox", cursor, 0, true); err != nil {
			return err
		}
		cursor = 0
		rows, hasMore, err = model.ListReceivedPointRefundInbox(0, options.BatchSize)
		if err != nil {
			return err
		}
	}
	for i, inbox := range rows {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		report.InboxScanned++
		_, processErr := ProcessPointRefundInbox(inbox.ID)
		if processErr == nil {
			report.InboxProcessed++
		} else if permanentRefundInboxError(processErr) {
			if err := model.MarkPointRefundInbox(inbox.ID, "quarantined", "evidence_mismatch", time.Now().UTC()); err != nil {
				report.InboxFailed++
			} else {
				report.InboxQuarantined++
			}
		} else {
			report.InboxFailed++
		}
		expected := cursor
		cursor = inbox.ID
		if err := model.AdvancePointRefundRecoveryCursor("inbox", expected, cursor, false); err != nil {
			return err
		}
		if i == len(rows)-1 && !hasMore {
			if err := model.AdvancePointRefundRecoveryCursor("inbox", cursor, 0, true); err != nil {
				return err
			}
		}
	}
	return nil
}

func permanentRefundInboxError(err error) bool {
	return errors.Is(err, model.ErrPointsConflict) || errors.Is(err, model.ErrPointRefundState)
}
