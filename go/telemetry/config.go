// Package telemetry registers OpenTelemetry observable instruments against
// command-station atomic snapshots. It is the only proto package that imports
// OTel. The SDK and OTLP exporter stay in the consumer (BigFred later).
package telemetry

import (
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const meterName = "github.com/dcc-bigfred/proto/go/telemetry"

// Config labels instruments. Attrs typically include layout.id and
// command_station.id from the consumer. Meter nil → global/no-op meter.
type Config struct {
	Meter metric.Meter
	Attrs []attribute.KeyValue
}

func (c Config) meter() metric.Meter {
	if c.Meter != nil {
		return c.Meter
	}
	return otel.Meter(meterName)
}
