package commandstation

import (
	"fmt"
	"net"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"
)

// lnTCPBinaryTransport speaks RAW LocoNet bytes over a TCP socket: every
// message is exchanged as its on-the-wire bytes (opcode … checksum
// inclusive), exactly like the serial transport but over TCP. There is no
// ASCII SEND/RECEIVE framing.
//
// This is the protocol RocRail's lbtcp client speaks
// (rocdigs/impl/loconet/lbtcp.c): it reads single bytes, derives the
// message length from the opcode and reassembles the frame. lnTCPASCIITransport
// (loconet_tcp.go) is the other RocRail variant — the ASCII LbServer
// protocol (rocdigs/impl/loconet/lbserver.c). Some gateways / command
// stations (e.g. raw LocoNet-over-TCP bridges) speak this binary form
// rather than LbServer, in which case the ASCII transport connects but
// every request times out because no `RECEIVE` line ever arrives.
type lnTCPBinaryTransport struct {
	core  *lnTCPConnHolder
	rxCh  chan<- lnPacket
	stop  chan struct{}
	rxBytes atomic.Uint64
}

func newLnTCPBinaryTransport(host string, port uint16, rxCh chan<- lnPacket) (*lnTCPBinaryTransport, error) {
	if host == "" {
		return nil, fmt.Errorf("loconet tcp (binary): host is empty")
	}
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	dial := func() (net.Conn, error) {
		return net.DialTimeout("tcp", addr, lnTCPDialTimeout)
	}
	conn, err := dial()
	if err != nil {
		return nil, fmt.Errorf("loconet tcp (binary): dial %s: %w", addr, err)
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(true)
	}
	t := &lnTCPBinaryTransport{
		core: newLnTCPConnHolder(addr, dial),
		rxCh: rxCh,
		stop: make(chan struct{}),
	}
	t.core.start(conn)
	logrus.WithField("addr", addr).Info("loconet command station: TCP (binary) connected")
	go t.readLoop()
	return t, nil
}

// lnTransportStats implements lnStatsTransport.
func (t *lnTCPBinaryTransport) lnTransportStats() lnTransportStatsSnapshot {
	s := t.core.transportStats()
	s.RxBytes = t.rxBytes.Load()
	return s
}

// RxByteCount reports how many bytes have been read off the socket since
// connect.
func (t *lnTCPBinaryTransport) RxByteCount() uint64 {
	return t.rxBytes.Load()
}

func (t *lnTCPBinaryTransport) WritePacket(pkt []byte) error {
	return t.core.writeWithDeadline(func(conn net.Conn) error {
		_, err := conn.Write(pkt)
		return err
	})
}

func (t *lnTCPBinaryTransport) Close() error {
	select {
	case <-t.stop:
	default:
		close(t.stop)
	}
	return t.core.close()
}

func (t *lnTCPBinaryTransport) readLoop() {
	var p lnStreamParser
	buf := make([]byte, 256)
	for {
		select {
		case <-t.stop:
			return
		default:
		}

		conn := t.core.connOrNil()
		if conn == nil {
			select {
			case <-t.stop:
				return
			case <-time.After(50 * time.Millisecond):
			}
			continue
		}

		n, err := conn.Read(buf)
		if n > 0 {
			t.rxBytes.Add(uint64(n))
			for i := 0; i < n; i++ {
				pkt, ok := p.PushByte(buf[i])
				if !ok {
					continue
				}
				if !lnChecksumOK(pkt) {
					logrus.Debugf("loconet tcp (binary): dropping packet (bad checksum): % X", pkt)
					continue
				}
				if !pushRxPacket(t.rxCh, t.stop, lnPacket(pkt)) {
					return
				}
			}
		}
		if err != nil {
			select {
			case <-t.stop:
				return
			default:
			}
			p = lnStreamParser{}
			t.core.signalReconnect()
			logrus.Debugf("loconet tcp (binary): read error, reconnecting: %v", err)
		}
	}
}
