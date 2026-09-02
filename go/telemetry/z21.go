package telemetry

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/dcc-bigfred/proto/go/commandstation"
)

const (
	mZ21Packets    = "proto.z21.packets"
	mZ21Bytes      = "proto.z21.bytes"
	mZ21Messages   = "proto.z21.messages"
	mZ21Dropped    = "proto.z21.dropped"
	mZ21Errors     = "proto.z21.errors"
	mZ21CVResults  = "proto.z21.cv_results"
	mZ21FnCache    = "proto.z21.fn_cache.entries"
	mZ21QueueDepth = "proto.z21.queue.depth"
)

// RegisterZ21 attaches observable instruments to src. Returns (nil, nil)
// when src is nil. Unregister the Registration on shutdown.
func RegisterZ21(src commandstation.Z21MetricsSource, cfg Config) (metric.Registration, error) {
	if src == nil {
		return nil, nil
	}
	meter := cfg.meter()
	base := cfg.Attrs

	packets, err := meter.Int64ObservableCounter(mZ21Packets,
		metric.WithDescription("Z21 packets transferred, by direction"),
		metric.WithUnit("{packet}"))
	if err != nil {
		return nil, fmt.Errorf("z21 packets counter: %w", err)
	}
	bytesC, err := meter.Int64ObservableCounter(mZ21Bytes,
		metric.WithDescription("Z21 bytes transferred, by direction"),
		metric.WithUnit("By"))
	if err != nil {
		return nil, fmt.Errorf("z21 bytes counter: %w", err)
	}
	messages, err := meter.Int64ObservableCounter(mZ21Messages,
		metric.WithDescription("Z21 messages by type and direction"),
		metric.WithUnit("{message}"))
	if err != nil {
		return nil, fmt.Errorf("z21 messages counter: %w", err)
	}
	dropped, err := meter.Int64ObservableCounter(mZ21Dropped,
		metric.WithDescription("Internal queue overflows, by queue"),
		metric.WithUnit("{event}"))
	if err != nil {
		return nil, fmt.Errorf("z21 dropped counter: %w", err)
	}
	errorsC, err := meter.Int64ObservableCounter(mZ21Errors,
		metric.WithDescription("Z21 errors, by kind"),
		metric.WithUnit("{error}"))
	if err != nil {
		return nil, fmt.Errorf("z21 errors counter: %w", err)
	}
	cvResults, err := meter.Int64ObservableCounter(mZ21CVResults,
		metric.WithDescription("Z21 CV programming outcomes, by kind"),
		metric.WithUnit("{event}"))
	if err != nil {
		return nil, fmt.Errorf("z21 cv_results counter: %w", err)
	}
	fnCache, err := meter.Int64ObservableGauge(mZ21FnCache,
		metric.WithDescription("Locomotives with cached function state"),
		metric.WithUnit("{loco}"))
	if err != nil {
		return nil, fmt.Errorf("z21 fn_cache gauge: %w", err)
	}
	queueDepth, err := meter.Int64ObservableGauge(mZ21QueueDepth,
		metric.WithDescription("Internal channel occupancy, by queue"),
		metric.WithUnit("{item}"))
	if err != nil {
		return nil, fmt.Errorf("z21 queue_depth gauge: %w", err)
	}

	withBase := func(extra ...attribute.KeyValue) metric.ObserveOption {
		attrs := make([]attribute.KeyValue, 0, len(base)+len(extra))
		attrs = append(attrs, base...)
		attrs = append(attrs, extra...)
		return metric.WithAttributes(attrs...)
	}

	cb := func(_ context.Context, o metric.Observer) error {
		s := src.Z21MetricsSnapshot()

		o.ObserveInt64(packets, int64(s.TxPackets), withBase(attribute.String("direction", "tx")))
		o.ObserveInt64(packets, int64(s.RxPackets), withBase(attribute.String("direction", "rx")))
		o.ObserveInt64(bytesC, int64(s.TxBytes), withBase(attribute.String("direction", "tx")))
		o.ObserveInt64(bytesC, int64(s.RxBytes), withBase(attribute.String("direction", "rx")))

		for typ, n := range s.TxByType {
			o.ObserveInt64(messages, int64(n),
				withBase(attribute.String("direction", "tx"), attribute.String("type", commandstation.Z21MsgTypeName(typ))))
		}
		for typ, n := range s.RxByType {
			o.ObserveInt64(messages, int64(n),
				withBase(attribute.String("direction", "rx"), attribute.String("type", commandstation.Z21MsgTypeName(typ))))
		}

		o.ObserveInt64(dropped, int64(s.ObsDropped), withBase(attribute.String("queue", "obs")))
		o.ObserveInt64(dropped, int64(s.SyncDropped), withBase(attribute.String("queue", "sync")))

		o.ObserveInt64(errorsC, int64(s.TxErrors), withBase(attribute.String("kind", "tx")))
		o.ObserveInt64(errorsC, int64(s.RxErrors), withBase(attribute.String("kind", "rx")))
		o.ObserveInt64(errorsC, int64(s.SyncTimeouts), withBase(attribute.String("kind", "sync_timeout")))

		o.ObserveInt64(cvResults, int64(s.CvNacks), withBase(attribute.String("kind", "nack")))
		o.ObserveInt64(cvResults, int64(s.CvNackSC), withBase(attribute.String("kind", "nack_sc")))

		o.ObserveInt64(fnCache, s.FnCacheEntries, withBase())

		o.ObserveInt64(queueDepth, s.ObsQueueLen, withBase(attribute.String("queue", "obs")))
		o.ObserveInt64(queueDepth, s.SyncQueueLen, withBase(attribute.String("queue", "sync")))
		return nil
	}

	reg, err := meter.RegisterCallback(cb,
		packets, bytesC, messages, dropped, errorsC, cvResults, fnCache, queueDepth,
	)
	if err != nil {
		return nil, fmt.Errorf("register z21 metrics callback: %w", err)
	}
	return reg, nil
}
