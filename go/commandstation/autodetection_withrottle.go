package commandstation

import (
	"context"
	"fmt"
	"time"

	"github.com/dcc-bigfred/proto/go/withrottle"
)

// WiThrottleAutodetection scans the local /24 subnet for a WiThrottle TCP
// service (JMRI / LNWI / RB1110 default port 12090).
type WiThrottleAutodetection struct {
	SubnetPrefix string
	Dial         TCPDialer
	DialTimeout  time.Duration
}

func (a WiThrottleAutodetection) Scan(ctx context.Context, emit EmitFunc) error {
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
	port := int(withrottle.DefaultPort)
	return scanTCPHosts(ctx, a.SubnetPrefix, []int{port}, dial, func(host string, p int) {
		_ = emit(DetectedConnection{
			Name: fmt.Sprintf("WiThrottle %s:%d", host, p),
			URI:  fmt.Sprintf("withrottle://%s:%d", host, p),
		})
	})
}
