package telemetry

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/dcc-bigfred/proto/go/commandstation"
)

const (
	mLNFrames      = "proto.loconet.frames"
	mLNBytes       = "proto.loconet.bytes"
	mLNMessages    = "proto.loconet.messages"
	mLNPaceWait    = "proto.loconet.pace_wait.seconds"
	mLNCoalesced   = "proto.loconet.coalesced"
	mLNDropped     = "proto.loconet.dropped"
	mLNErrors      = "proto.loconet.errors"
	mLNSlotOps     = "proto.loconet.slot_ops"
	mLNSlotsActive = "proto.loconet.slots.active"
	mLNQueueDepth  = "proto.loconet.queue.depth"
)

// RegisterLocoNet attaches observable instruments to src. Returns (nil, nil)
// when src is nil. Unregister the Registration on shutdown.
func RegisterLocoNet(src commandstation.MetricsSource, cfg Config) (metric.Registration, error) {
	if src == nil {
		return nil, nil
	}
	meter := cfg.meter()
	base := cfg.Attrs

	frames, err := meter.Int64ObservableCounter(mLNFrames,
		metric.WithDescription("LocoNet frames transferred, by direction"),
		metric.WithUnit("{frame}"))
	if err != nil {
		return nil, fmt.Errorf("loconet frames counter: %w", err)
	}
	bytesC, err := meter.Int64ObservableCounter(mLNBytes,
		metric.WithDescription("LocoNet bytes transferred, by direction"),
		metric.WithUnit("By"))
	if err != nil {
		return nil, fmt.Errorf("loconet bytes counter: %w", err)
	}
	messages, err := meter.Int64ObservableCounter(mLNMessages,
		metric.WithDescription("LocoNet messages by opcode and direction"),
		metric.WithUnit("{message}"))
	if err != nil {
		return nil, fmt.Errorf("loconet messages counter: %w", err)
	}
	paceWait, err := meter.Float64ObservableCounter(mLNPaceWait,
		metric.WithDescription("Cumulative time the TX pacer blocked (bus saturation indicator)"),
		metric.WithUnit("s"))
	if err != nil {
		return nil, fmt.Errorf("loconet pace_wait counter: %w", err)
	}
	coalesced, err := meter.Int64ObservableCounter(mLNCoalesced,
		metric.WithDescription("Speed frames dropped because a newer SetSpeed superseded them"),
		metric.WithUnit("{frame}"))
	if err != nil {
		return nil, fmt.Errorf("loconet coalesced counter: %w", err)
	}
	dropped, err := meter.Int64ObservableCounter(mLNDropped,
		metric.WithDescription("Internal queue overflows, by queue"),
		metric.WithUnit("{event}"))
	if err != nil {
		return nil, fmt.Errorf("loconet dropped counter: %w", err)
	}
	errorsC, err := meter.Int64ObservableCounter(mLNErrors,
		metric.WithDescription("LocoNet errors, by kind"),
		metric.WithUnit("{error}"))
	if err != nil {
		return nil, fmt.Errorf("loconet errors counter: %w", err)
	}
	slotOps, err := meter.Int64ObservableCounter(mLNSlotOps,
		metric.WithDescription("LocoNet slot lifecycle operations, by op"),
		metric.WithUnit("{operation}"))
	if err != nil {
		return nil, fmt.Errorf("loconet slot_ops counter: %w", err)
	}
	slotsActive, err := meter.Int64ObservableGauge(mLNSlotsActive,
		metric.WithDescription("LocoNet slots currently owned by this daemon"),
		metric.WithUnit("{slot}"))
	if err != nil {
		return nil, fmt.Errorf("loconet slots_active gauge: %w", err)
	}
	queueDepth, err := meter.Int64ObservableGauge(mLNQueueDepth,
		metric.WithDescription("Internal channel occupancy, by queue"),
		metric.WithUnit("{item}"))
	if err != nil {
		return nil, fmt.Errorf("loconet queue_depth gauge: %w", err)
	}

	withBase := func(extra ...attribute.KeyValue) metric.ObserveOption {
		attrs := make([]attribute.KeyValue, 0, len(base)+len(extra))
		attrs = append(attrs, base...)
		attrs = append(attrs, extra...)
		return metric.WithAttributes(attrs...)
	}

	cb := func(_ context.Context, o metric.Observer) error {
		s := src.MetricsSnapshot()

		o.ObserveInt64(frames, int64(s.TxFrames), withBase(attribute.String("direction", "tx")))
		o.ObserveInt64(frames, int64(s.RxFrames), withBase(attribute.String("direction", "rx")))
		o.ObserveInt64(bytesC, int64(s.TxBytes), withBase(attribute.String("direction", "tx")))
		o.ObserveInt64(bytesC, int64(s.RxBytes), withBase(attribute.String("direction", "rx")))

		for op, n := range s.TxByOpcode {
			o.ObserveInt64(messages, int64(n),
				withBase(attribute.String("direction", "tx"), attribute.String("opcode", commandstation.LnOpcodeName(op))))
		}
		for op, n := range s.RxByOpcode {
			o.ObserveInt64(messages, int64(n),
				withBase(attribute.String("direction", "rx"), attribute.String("opcode", commandstation.LnOpcodeName(op))))
		}

		o.ObserveFloat64(paceWait, s.PaceWaitSeconds, withBase())
		o.ObserveInt64(coalesced, int64(s.TxCoalesced), withBase())

		o.ObserveInt64(dropped, int64(s.ObsDropped), withBase(attribute.String("queue", "obs")))
		o.ObserveInt64(dropped, int64(s.SyncDropped), withBase(attribute.String("queue", "sync")))

		o.ObserveInt64(errorsC, int64(s.TxErrors), withBase(attribute.String("kind", "tx")))
		o.ObserveInt64(errorsC, int64(s.BadChecksum), withBase(attribute.String("kind", "bad_checksum")))
		o.ObserveInt64(errorsC, int64(s.Reconnects), withBase(attribute.String("kind", "reconnect")))
		o.ObserveInt64(errorsC, int64(s.WriteTimeouts), withBase(attribute.String("kind", "write_timeout")))
		o.ObserveInt64(errorsC, int64(s.LackRejections), withBase(attribute.String("kind", "lack_reject")))

		o.ObserveInt64(slotOps, int64(s.SlotAcquires), withBase(attribute.String("op", "acquire")))
		o.ObserveInt64(slotOps, int64(s.SlotAcquireFails), withBase(attribute.String("op", "acquire_fail")))
		o.ObserveInt64(slotOps, int64(s.SlotRetries), withBase(attribute.String("op", "retry")))
		o.ObserveInt64(slotOps, int64(s.SlotReleases), withBase(attribute.String("op", "release")))
		o.ObserveInt64(slotOps, int64(s.SlotDispatches), withBase(attribute.String("op", "dispatch")))
		o.ObserveInt64(slotOps, int64(s.KeepaliveRefresh), withBase(attribute.String("op", "keepalive")))
		o.ObserveInt64(slotOps, int64(s.CsSlotOccupied), withBase(attribute.String("op", "cs_occupied")))
		o.ObserveInt64(slotOps, int64(s.CsSlotReleased), withBase(attribute.String("op", "cs_released")))

		o.ObserveInt64(slotsActive, s.SlotsActive, withBase())

		o.ObserveInt64(queueDepth, s.RxQueueLen, withBase(attribute.String("queue", "rx")))
		o.ObserveInt64(queueDepth, s.ObsQueueLen, withBase(attribute.String("queue", "obs")))
		o.ObserveInt64(queueDepth, s.SyncQueueLen, withBase(attribute.String("queue", "sync")))
		o.ObserveInt64(queueDepth, s.TxQueueLen, withBase(attribute.String("queue", "tx")))
		return nil
	}

	reg, err := meter.RegisterCallback(cb,
		frames, bytesC, messages, paceWait, coalesced, dropped, errorsC,
		slotOps, slotsActive, queueDepth,
	)
	if err != nil {
		return nil, fmt.Errorf("register loconet metrics callback: %w", err)
	}
	return reg, nil
}
