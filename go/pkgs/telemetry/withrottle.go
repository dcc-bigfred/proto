package telemetry

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/metric"

	"github.com/dcc-bigfred/proto/go/pkgs/withrottle"
)

const (
	mWTLinesTx    = "proto.withrottle.lines.tx"
	mWTLinesRx    = "proto.withrottle.lines.rx"
	mWTAcquireTO  = "proto.withrottle.acquire.timeouts"
	mWTHeartbeats = "proto.withrottle.heartbeats"
	mWTLastRxAge  = "proto.withrottle.last_rx_age"
)

// RegisterWithrottle attaches observable instruments to src. Returns (nil, nil)
// when src is nil.
func RegisterWithrottle(src withrottle.MetricsSource, cfg Config) (metric.Registration, error) {
	if src == nil {
		return nil, nil
	}
	meter := cfg.meter()
	base := cfg.Attrs

	linesTx, err := meter.Int64ObservableCounter(mWTLinesTx,
		metric.WithDescription("WiThrottle lines written"),
		metric.WithUnit("{line}"))
	if err != nil {
		return nil, fmt.Errorf("withrottle lines.tx: %w", err)
	}
	linesRx, err := meter.Int64ObservableCounter(mWTLinesRx,
		metric.WithDescription("WiThrottle lines read"),
		metric.WithUnit("{line}"))
	if err != nil {
		return nil, fmt.Errorf("withrottle lines.rx: %w", err)
	}
	timeouts, err := meter.Int64ObservableCounter(mWTAcquireTO,
		metric.WithDescription("WiThrottle acquire timeouts"),
		metric.WithUnit("{event}"))
	if err != nil {
		return nil, fmt.Errorf("withrottle acquire.timeouts: %w", err)
	}
	heartbeats, err := meter.Int64ObservableCounter(mWTHeartbeats,
		metric.WithDescription("WiThrottle heartbeats sent"),
		metric.WithUnit("{event}"))
	if err != nil {
		return nil, fmt.Errorf("withrottle heartbeats: %w", err)
	}
	lastAge, err := meter.Int64ObservableGauge(mWTLastRxAge,
		metric.WithDescription("Age of last received WiThrottle line"),
		metric.WithUnit("ns"))
	if err != nil {
		return nil, fmt.Errorf("withrottle last_rx_age: %w", err)
	}

	withBase := func() metric.ObserveOption {
		if len(base) == 0 {
			return metric.WithAttributes()
		}
		return metric.WithAttributes(base...)
	}

	cb := func(_ context.Context, o metric.Observer) error {
		s := src.Metrics()
		opt := withBase()
		o.ObserveInt64(linesTx, int64(s.LinesTx), opt)
		o.ObserveInt64(linesRx, int64(s.LinesRx), opt)
		o.ObserveInt64(timeouts, int64(s.AcquireTimeouts), opt)
		o.ObserveInt64(heartbeats, int64(s.HeartbeatsSent), opt)
		var age int64
		if s.LastRxUnixNano > 0 {
			age = time.Now().UnixNano() - s.LastRxUnixNano
			if age < 0 {
				age = 0
			}
		}
		o.ObserveInt64(lastAge, age, opt)
		return nil
	}

	reg, err := meter.RegisterCallback(cb, linesTx, linesRx, timeouts, heartbeats, lastAge)
	if err != nil {
		return nil, fmt.Errorf("register withrottle metrics callback: %w", err)
	}
	return reg, nil
}
