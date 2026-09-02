package commandstation

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

// TCP transport in LoconetOverTcp style:
// - client sends lines: "SEND <hex bytes...>\r\n"
// - server sends lines: "RECEIVE <hex bytes...>\r\n"
// See: https://loconetovertcp.sourceforge.net/Protocol/LoconetOverTcp.html
type lnTCPASCIITransport struct {
	core *lnTCPConnHolder
	rxCh chan<- lnPacket
	stop chan struct{}
}

func newLnTCPASCIITransport(host string, port uint16, rxCh chan<- lnPacket) (*lnTCPASCIITransport, error) {
	if host == "" {
		return nil, fmt.Errorf("loconet tcp: host is empty")
	}
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	dial := func() (net.Conn, error) {
		return net.DialTimeout("tcp", addr, lnTCPDialTimeout)
	}
	conn, err := dial()
	if err != nil {
		return nil, fmt.Errorf("loconet tcp: dial %s: %w", addr, err)
	}
	t := &lnTCPASCIITransport{
		core: newLnTCPConnHolder(addr, dial),
		rxCh: rxCh,
		stop: make(chan struct{}),
	}
	t.core.start(conn)
	logrus.WithField("addr", addr).Info("loconet command station: TCP connected")
	go t.readLoop()
	return t, nil
}

// lnTransportStats implements lnStatsTransport.
func (t *lnTCPASCIITransport) lnTransportStats() lnTransportStatsSnapshot {
	return t.core.transportStats()
}

func (t *lnTCPASCIITransport) WritePacket(pkt []byte) error {
	if !lnChecksumOK(pkt) {
		return fmt.Errorf("loconet tcp: invalid checksum, refusing SEND: % X", pkt)
	}
	var sb strings.Builder
	sb.WriteString("SEND")
	for _, b := range pkt {
		sb.WriteString(fmt.Sprintf(" %02X", b))
	}
	sb.WriteString("\r\n")
	line := sb.String()
	return t.core.writeWithDeadline(func(conn net.Conn) error {
		w := bufio.NewWriter(conn)
		if _, err := w.WriteString(line); err != nil {
			return err
		}
		return w.Flush()
	})
}

func (t *lnTCPASCIITransport) Close() error {
	select {
	case <-t.stop:
	default:
		close(t.stop)
	}
	return t.core.close()
}

func (t *lnTCPASCIITransport) readLoop() {
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

		r := bufio.NewReader(conn)
		line, err := r.ReadString('\n')
		if line != "" {
			t.handleLine(line)
		}
		if err != nil {
			select {
			case <-t.stop:
				return
			default:
			}
			t.core.signalReconnect()
			logrus.Debugf("loconet tcp: read error, reconnecting: %v", err)
		}
	}
}

// handleLine parses one LoconetOverTcp protocol line. It forwards RECEIVE
// payloads (a LocoNet message in space-separated hex) onto rxCh and logs
// every other token (VERSION, SENT, ERROR, TIMESTAMP, …).
func (t *lnTCPASCIITransport) handleLine(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	const recv = "RECEIVE "
	idx := strings.Index(line, recv)
	if idx < 0 {
		logrus.Debugf("loconet tcp: %s", line)
		return
	}
	hexPart := strings.TrimSpace(line[idx+len(recv):])
	pkt, err := lnParseHexBytes(hexPart)
	if err != nil {
		logrus.Debugf("loconet tcp: cannot parse RECEIVE %q: %v", line, err)
		return
	}
	if n, ok := lnMsgLen(pkt[0], pkt); ok && n >= 2 && n <= len(pkt) {
		pkt = pkt[:n]
	}
	if !lnChecksumOK(pkt) {
		logrus.Debugf("loconet tcp: dropping packet (bad checksum): % X", pkt)
		return
	}
	pushRxPacket(t.rxCh, t.stop, lnPacket(pkt))
}
