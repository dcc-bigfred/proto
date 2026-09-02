package commandstation

import (
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"
)

const (
	lnTCPWriteTimeout     = 2 * time.Second
	lnTCPReconnectBackoff = time.Second
	lnTCPDialTimeout      = 5 * time.Second
)

var errTCPNotConnected = errors.New("loconet tcp: not connected (reconnecting)")

// lnTCPConnHolder owns a TCP connection with automatic reconnect on read/write
// failure, mirroring the serial transport supervisor. readLoop goroutines call
// signalReconnect when the peer closes or resets; WritePacket uses a write
// deadline so a wedged peer cannot block the fleet behind txMu forever.
type lnTCPConnHolder struct {
	addr string
	dial func() (net.Conn, error)

	mu          sync.Mutex
	conn        net.Conn
	writeMu     sync.Mutex
	reconnectCh chan struct{}
	stop        chan struct{}

	reconnects    atomic.Uint64
	writeTimeouts atomic.Uint64
	writeErrors   atomic.Uint64
}

func newLnTCPConnHolder(addr string, dial func() (net.Conn, error)) *lnTCPConnHolder {
	return &lnTCPConnHolder{
		addr:        addr,
		dial:        dial,
		reconnectCh: make(chan struct{}, 1),
		stop:        make(chan struct{}),
	}
}

func (h *lnTCPConnHolder) start(conn net.Conn) {
	h.mu.Lock()
	h.conn = conn
	h.mu.Unlock()
	go h.supervisor()
}

func (h *lnTCPConnHolder) connOrNil() net.Conn {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.conn
}

func (h *lnTCPConnHolder) signalReconnect() {
	select {
	case h.reconnectCh <- struct{}{}:
	default:
	}
}

func (h *lnTCPConnHolder) close() error {
	select {
	case <-h.stop:
	default:
		close(h.stop)
	}
	h.mu.Lock()
	conn := h.conn
	h.conn = nil
	h.mu.Unlock()
	if conn != nil {
		return conn.Close()
	}
	return nil
}

func (h *lnTCPConnHolder) supervisor() {
	for {
		select {
		case <-h.stop:
			return
		case <-h.reconnectCh:
			h.doReconnect()
		}
	}
}

func (h *lnTCPConnHolder) doReconnect() {
	h.mu.Lock()
	old := h.conn
	h.conn = nil
	h.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}

	for {
		select {
		case <-h.stop:
			return
		default:
		}
		conn, err := h.dial()
		if err != nil {
			logrus.WithError(err).WithField("addr", h.addr).
				Warn("loconet tcp: reconnect failed, retrying")
			select {
			case <-h.stop:
				return
			case <-time.After(lnTCPReconnectBackoff):
			}
			continue
		}
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.SetNoDelay(true)
		}
		h.mu.Lock()
		h.conn = conn
		h.mu.Unlock()
		h.reconnects.Add(1)
		logrus.WithField("addr", h.addr).Info("loconet tcp: reconnected")
		return
	}
}

// writeWithDeadline runs fn against the current connection under writeMu with
// a bounded write deadline. The deadline makes conn.Write return within
// lnTCPWriteTimeout even on a wedged peer, so the single txLoop is never
// blocked longer than that. On failure it triggers reconnect.
func (h *lnTCPConnHolder) writeWithDeadline(fn func(net.Conn) error) error {
	h.writeMu.Lock()
	defer h.writeMu.Unlock()

	h.mu.Lock()
	conn := h.conn
	h.mu.Unlock()
	if conn == nil {
		return errTCPNotConnected
	}

	_ = conn.SetWriteDeadline(time.Now().Add(lnTCPWriteTimeout))
	err := fn(conn)
	_ = conn.SetWriteDeadline(time.Time{})
	if err != nil {
		h.writeErrors.Add(1)
		if t, ok := err.(interface{ Timeout() bool }); ok && t.Timeout() {
			h.writeTimeouts.Add(1)
		}
		h.signalReconnect()
	}
	return err
}

func (h *lnTCPConnHolder) transportStats() lnTransportStatsSnapshot {
	return lnTransportStatsSnapshot{
		Reconnects:    h.reconnects.Load(),
		WriteTimeouts: h.writeTimeouts.Load(),
		WriteErrors:   h.writeErrors.Load(),
	}
}

// pushRxPacket forwards a validated packet to rxCh, honouring stop.
func pushRxPacket(rxCh chan<- lnPacket, stop <-chan struct{}, pkt lnPacket) bool {
	select {
	case rxCh <- pkt:
		return true
	case <-stop:
		return false
	}
}
