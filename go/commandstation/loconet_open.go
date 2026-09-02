package commandstation

import (
	"sync"

	"github.com/dcc-bigfred/proto/go/loconet"
)

// LocoNetTransport is a raw LocoNet end a Gateway can own. It matches
// loconet.Upstream structurally so NewGateway(OpenLocoNetSerial(...)) type-checks.
type LocoNetTransport interface {
	WritePacket(pkt []byte) error
	Recv() <-chan []byte
	Close() error
}

var _ loconet.Upstream = (*publicTransport)(nil)
var _ LocoNetTransport = (*publicTransport)(nil)

type publicTransport struct {
	inner lnTransport
	out   chan []byte
	stop  chan struct{}
	once  sync.Once
}

func wrapPublic(inner lnTransport, rx <-chan lnPacket) *publicTransport {
	t := &publicTransport{
		inner: inner,
		out:   make(chan []byte, 64),
		stop:  make(chan struct{}),
	}
	go func() {
		defer close(t.out)
		for {
			select {
			case pkt, ok := <-rx:
				if !ok {
					return
				}
				cp := append([]byte(nil), pkt...)
				select {
				case t.out <- cp:
				case <-t.stop:
					return
				}
			case <-t.stop:
				return
			}
		}
	}()
	return t
}

func (t *publicTransport) WritePacket(pkt []byte) error { return t.inner.WritePacket(pkt) }

func (t *publicTransport) Recv() <-chan []byte { return t.out }

func (t *publicTransport) Close() error {
	t.once.Do(func() { close(t.stop) })
	return t.inner.Close()
}

// OpenLocoNetSerial opens a serial LocoNet port for use as a gateway upstream.
func OpenLocoNetSerial(device string, baudrate int) (LocoNetTransport, error) {
	rx := make(chan lnPacket, 64)
	inner, err := newLnSerialTransport(device, baudrate, rx)
	if err != nil {
		return nil, err
	}
	return wrapPublic(inner, rx), nil
}

// OpenLocoNetTCP opens an LbServer (ASCII) TCP client for use as a gateway upstream.
func OpenLocoNetTCP(host string, port uint16) (LocoNetTransport, error) {
	rx := make(chan lnPacket, 64)
	inner, err := newLnTCPASCIITransport(host, port, rx)
	if err != nil {
		return nil, err
	}
	return wrapPublic(inner, rx), nil
}

// OpenLocoNetTCPBinary opens a raw LocoNet-over-TCP client for use as a gateway upstream.
func OpenLocoNetTCPBinary(host string, port uint16) (LocoNetTransport, error) {
	rx := make(chan lnPacket, 64)
	inner, err := newLnTCPBinaryTransport(host, port, rx)
	if err != nil {
		return nil, err
	}
	return wrapPublic(inner, rx), nil
}
