package loconet

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
	"sync"
)

// Mode selects downstream TCP framing.
type Mode int

const (
	// Binary is raw LocoNet bytes (RocRail lbtcp).
	Binary Mode = iota
	// ASCII is LoconetOverTcp SEND/RECEIVE lines (LbServer).
	ASCII
)

// Upstream is the single physical (or test) LocoNet end. Recv may be nil
// when the upstream is write-only.
type Upstream interface {
	WritePacket(pkt []byte) error
	Recv() <-chan []byte
	Close() error
}

// Gateway fans every well-formed frame from any end to the others.
type Gateway struct {
	up Upstream

	mu        sync.Mutex
	listeners []net.Listener
	conns     []*gwConn
	closed    bool
}

type gwConn struct {
	c    net.Conn
	mode Mode
}

// NewGateway wraps one upstream. up may be nil (listener-only hub).
func NewGateway(up Upstream) *Gateway {
	g := &Gateway{up: up}
	if up != nil {
		if ch := up.Recv(); ch != nil {
			go g.upstreamLoop(ch)
		}
	}
	return g
}

// Listen binds TCP and serves until Close. bind may be "127.0.0.1:0".
func (g *Gateway) Listen(bind string, mode Mode) (net.Addr, error) {
	ln, err := net.Listen("tcp", bind)
	if err != nil {
		return nil, err
	}
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		_ = ln.Close()
		return nil, fmt.Errorf("loconet: gateway closed")
	}
	g.listeners = append(g.listeners, ln)
	g.mu.Unlock()
	go g.acceptLoop(ln, mode)
	return ln.Addr(), nil
}

// Close stops listeners, downstreams, and the upstream.
func (g *Gateway) Close() error {
	g.mu.Lock()
	g.closed = true
	lns := append([]net.Listener(nil), g.listeners...)
	conns := append([]*gwConn(nil), g.conns...)
	g.mu.Unlock()
	for _, ln := range lns {
		_ = ln.Close()
	}
	for _, c := range conns {
		_ = c.c.Close()
	}
	if g.up != nil {
		return g.up.Close()
	}
	return nil
}

func (g *Gateway) acceptLoop(ln net.Listener, mode Mode) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		gc := &gwConn{c: conn, mode: mode}
		g.mu.Lock()
		g.conns = append(g.conns, gc)
		g.mu.Unlock()
		if mode == ASCII {
			_, _ = conn.Write([]byte("VERSION proto LocoNetOverTCP 1.0\r\n"))
		}
		go g.serve(gc)
	}
}

func (g *Gateway) serve(gc *gwConn) {
	defer g.drop(gc)
	if gc.mode == Binary {
		g.serveBinary(gc)
		return
	}
	g.serveASCII(gc)
}

func (g *Gateway) serveBinary(gc *gwConn) {
	var p StreamParser
	buf := make([]byte, 256)
	for {
		n, err := gc.c.Read(buf)
		for i := 0; i < n; i++ {
			pkt, ok := p.PushByte(buf[i])
			if !ok {
				continue
			}
			if !ChecksumOK(pkt) {
				continue
			}
			g.fanout(pkt, gc)
		}
		if err != nil {
			return
		}
	}
}

func (g *Gateway) serveASCII(gc *gwConn) {
	r := bufio.NewReader(gc.c)
	for {
		line, err := r.ReadString('\n')
		if line != "" {
			g.handleASCII(gc, line)
		}
		if err != nil {
			return
		}
	}
}

func (g *Gateway) handleASCII(gc *gwConn, line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	const send = "SEND"
	if !strings.HasPrefix(strings.ToUpper(line), send) {
		return
	}
	hexPart := strings.TrimSpace(line[len(send):])
	pkt, err := parseHexBytes(hexPart)
	if err != nil || !ChecksumOK(pkt) {
		_, _ = gc.c.Write([]byte("SENT ERROR checksum\r\n"))
		return
	}
	if n, ok := MsgLen(pkt[0], pkt); ok && n >= 2 && n <= len(pkt) {
		pkt = pkt[:n]
	}
	_, _ = gc.c.Write([]byte("SENT OK\r\n"))
	g.fanout(pkt, gc)
}

func (g *Gateway) upstreamLoop(ch <-chan []byte) {
	for pkt := range ch {
		if !ChecksumOK(pkt) {
			continue
		}
		g.fanout(pkt, nil)
	}
}

func (g *Gateway) fanout(pkt []byte, except *gwConn) {
	cp := append([]byte(nil), pkt...)
	if g.up != nil && except != nil {
		_ = g.up.WritePacket(cp)
	}
	g.mu.Lock()
	conns := append([]*gwConn(nil), g.conns...)
	g.mu.Unlock()
	for _, c := range conns {
		if c == except {
			continue
		}
		g.writeDown(c, cp)
	}
}

func (g *Gateway) writeDown(c *gwConn, pkt []byte) {
	if c.mode == Binary {
		_, _ = c.c.Write(pkt)
		return
	}
	var sb strings.Builder
	sb.WriteString("RECEIVE")
	for _, b := range pkt {
		sb.WriteString(fmt.Sprintf(" %02X", b))
	}
	sb.WriteString("\r\n")
	_, _ = c.c.Write([]byte(sb.String()))
}

func (g *Gateway) drop(gc *gwConn) {
	_ = gc.c.Close()
	g.mu.Lock()
	defer g.mu.Unlock()
	out := g.conns[:0]
	for _, c := range g.conns {
		if c != gc {
			out = append(out, c)
		}
	}
	g.conns = out
}

func parseHexBytes(s string) ([]byte, error) {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return nil, fmt.Errorf("empty hex list")
	}
	out := make([]byte, 0, len(fields))
	for _, f := range fields {
		if len(f) == 1 {
			f = "0" + f
		}
		if len(f) != 2 {
			return nil, fmt.Errorf("invalid hex byte %q", f)
		}
		dec, err := hex.DecodeString(f)
		if err != nil || len(dec) != 1 {
			return nil, fmt.Errorf("invalid hex byte %q", f)
		}
		out = append(out, dec[0])
	}
	return out, nil
}

// Recorder is an in-memory Upstream for tests.
type Recorder struct {
	mu      sync.Mutex
	Written [][]byte
	ch      chan []byte
}

// NewRecorder returns a recorder with a buffered Recv channel.
func NewRecorder() *Recorder {
	return &Recorder{ch: make(chan []byte, 16)}
}

// WritePacket records a copy of pkt.
func (r *Recorder) WritePacket(pkt []byte) error {
	r.mu.Lock()
	r.Written = append(r.Written, append([]byte(nil), pkt...))
	r.mu.Unlock()
	return nil
}

// Recv is the upstream RX channel.
func (r *Recorder) Recv() <-chan []byte { return r.ch }

// Inject delivers a frame as if it came from the physical bus.
func (r *Recorder) Inject(pkt []byte) {
	r.ch <- append([]byte(nil), pkt...)
}

// Close is a no-op besides closing Recv.
func (r *Recorder) Close() error {
	close(r.ch)
	return nil
}
