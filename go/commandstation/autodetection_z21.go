package commandstation

import (
	"context"
	"fmt"
	"sync"
	"time"
)

const (
	z21DefaultPort  = 21105
	z21DefaultOctet = 111
)

// LAN_GET_SERIAL_NUMBER request used as a lightweight Z21 reachability probe.
var z21SerialNumberProbe = []byte{0x04, 0x00, 0x10, 0x00}

// Z21Autodetection probes for a Z21 (or compatible) UDP service on port 21105.
// It tries subnet.111 first; on success it skips the full subnet scan.
type Z21Autodetection struct {
	SubnetPrefix string
	Probe        UDPProber
	ProbeTimeout time.Duration
}

func (a Z21Autodetection) Scan(ctx context.Context, emit EmitFunc) error {
	if a.SubnetPrefix == "" {
		return nil
	}
	if emit == nil {
		emit = func(DetectedConnection) error { return nil }
	}
	timeout := a.ProbeTimeout
	if timeout <= 0 {
		timeout = defaultLANDialTimeout
	}
	probe := a.Probe
	if probe == nil {
		probe = defaultUDPProber(timeout)
	}

	preferred := fmt.Sprintf("%s.%d", a.SubnetPrefix, z21DefaultOctet)
	if err := ctx.Err(); err != nil {
		return scanContextError(ctx)
	}
	if err := probe(ctx, fmt.Sprintf("%s:%d", preferred, z21DefaultPort), z21SerialNumberProbe); err == nil {
		return emit(DetectedConnection{
			Name: fmt.Sprintf("Z21 %s:%d", preferred, z21DefaultPort),
			URI:  fmt.Sprintf("udp://%s:%d", preferred, z21DefaultPort),
		})
	}

	jobs := make(chan string)
	var wg sync.WaitGroup
	workers := defaultLANWorkers
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for host := range jobs {
				if ctx.Err() != nil {
					continue
				}
				addr := fmt.Sprintf("%s:%d", host, z21DefaultPort)
				if err := probe(ctx, addr, z21SerialNumberProbe); err != nil {
					continue
				}
				_ = emit(DetectedConnection{
					Name: fmt.Sprintf("Z21 %s:%d", host, z21DefaultPort),
					URI:  fmt.Sprintf("udp://%s:%d", host, z21DefaultPort),
				})
			}
		}()
	}

	go func() {
		defer close(jobs)
		for i := 1; i <= 255; i++ {
			host := fmt.Sprintf("%s.%d", a.SubnetPrefix, i)
			select {
			case <-ctx.Done():
				return
			case jobs <- host:
			}
		}
	}()

	wg.Wait()
	return scanContextError(ctx)
}
