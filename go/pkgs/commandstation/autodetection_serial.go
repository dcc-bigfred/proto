package commandstation

import (
	"context"
	"fmt"
)

// Common Uhlenbrock 63120 / USB-serial baud rates offered as connection variants.
var locoNetSerialBaudRates = []int{19200, 38400, 57600, 115200}

// LocoNetSerialAutodetection lists local serial ports (and autodetect URIs)
// as LocoNet connection options for each common baud rate.
type LocoNetSerialAutodetection struct{}

func (LocoNetSerialAutodetection) Scan(ctx context.Context, emit EmitFunc) error {
	if err := ctx.Err(); err != nil {
		return scanContextError(ctx)
	}
	if emit == nil {
		emit = func(DetectedConnection) error { return nil }
	}

	for _, baud := range locoNetSerialBaudRates {
		if err := ctx.Err(); err != nil {
			return scanContextError(ctx)
		}
		if err := emit(DetectedConnection{
			Name: fmt.Sprintf("LocoNet Serial autodetect @ %d", baud),
			URI:  fmt.Sprintf("serial://%s:%d", SerialAutodetectDevice, baud),
		}); err != nil {
			return err
		}
	}

	candidates, err := listSerialCandidates()
	if err != nil {
		return err
	}
	for _, device := range candidates {
		if err := ctx.Err(); err != nil {
			return scanContextError(ctx)
		}
		for _, baud := range locoNetSerialBaudRates {
			if err := emit(DetectedConnection{
				Name: fmt.Sprintf("LocoNet Serial %s @ %d", device, baud),
				URI:  fmt.Sprintf("serial://%s:%d", device, baud),
			}); err != nil {
				return err
			}
		}
	}
	return nil
}
