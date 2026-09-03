package commandstation

import (
	"context"
	"fmt"
	"time"
)

const (
	locoNetTCPBinaryPort = 1234
	locoNetTCPASCIIPort  = 5550
)

// LocoNetTCPAutodetection scans the local /24 subnet for LocoNet-over-TCP
// services (raw binary on 1234, LbServer ASCII on 5550).
type LocoNetTCPAutodetection struct {
	// SubnetPrefix is the first three IPv4 octets, e.g. "192.168.0".
	SubnetPrefix string
	Dial         TCPDialer
	DialTimeout  time.Duration
}

func (a LocoNetTCPAutodetection) Scan(ctx context.Context, emit EmitFunc) error {
	if a.SubnetPrefix == "" {
		return nil
	}
	if emit == nil {
		emit = func(DetectedConnection) error { return nil }
	}
	timeout := a.DialTimeout
	if timeout <= 0 {
		timeout = defaultLANDialTimeout
	}
	dial := a.Dial
	if dial == nil {
		dial = defaultTCPDialer(timeout)
	}

	return scanTCPHosts(ctx, a.SubnetPrefix, []int{locoNetTCPBinaryPort, locoNetTCPASCIIPort}, dial, func(host string, port int) {
		var c DetectedConnection
		switch port {
		case locoNetTCPBinaryPort:
			c = DetectedConnection{
				Name: fmt.Sprintf("LocoNet TCP Binary %s:%d", host, port),
				URI:  fmt.Sprintf("tcp://%s:%d", host, port),
			}
		case locoNetTCPASCIIPort:
			c = DetectedConnection{
				Name: fmt.Sprintf("LocoNet TCP ASCII %s:%d", host, port),
				URI:  fmt.Sprintf("lbserver://%s:%d", host, port),
			}
		default:
			return
		}
		_ = emit(c)
	})
}
