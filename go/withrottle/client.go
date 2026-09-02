package withrottle

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ErrAcquireTimeout is returned when the server never acks M+ within acquireTimeout.
var ErrAcquireTimeout = errors.New("withrottle: acquire timeout")

// ErrUnsupported is returned for operations WiThrottle cannot express (CV programming).
var ErrUnsupported = errors.New("withrottle: operation not supported")

// ErrStealRefused is returned when the server rejects an acquire with HMSteal.
var ErrStealRefused = errors.New("withrottle: steal not supported")

const acquireTimeout = 2 * time.Second

// Option configures NewClient.
type Option func(*clientOpts)

type clientOpts struct {
	deviceID string
	name     string
}

// WithDeviceID sets the HU identity (default proto-<8 hex>).
func WithDeviceID(id string) Option {
	return func(o *clientOpts) { o.deviceID = id }
}

// WithName sets the N name (default "proto").
func WithName(name string) Option {
	return func(o *clientOpts) { o.name = name }
}

// Snapshot is lock-free client counters for telemetry.RegisterWithrottle.
type Snapshot struct {
	LinesTx         uint64
	LinesRx         uint64
	AcquireTimeouts uint64
	HeartbeatsSent  uint64
	LastRxUnixNano  int64
}

// MetricsSource is implemented by *Client.
type MetricsSource interface {
	Metrics() Snapshot
}

// Client is a WiThrottle TCP throttle (JMRI / DCC-EX / LNWI / RB1110).
// commandstation.NewWiThrottle wraps it as a Station.
type Client struct {
	conn net.Conn
	r    *bufio.Reader
	wmu  sync.Mutex

	mu       sync.Mutex
	acquired map[uint16]struct{}
	pending  map[uint16]chan struct{}
	speed    map[uint16]uint8
	forward  map[uint16]bool
	fn       map[uint16]uint32
	steal    atomic.Bool
	hbPeriod time.Duration
	errCh    chan error
	closed   chan struct{}

	linesTx         atomic.Uint64
	linesRx         atomic.Uint64
	acquireTimeouts atomic.Uint64
	heartbeatsSent  atomic.Uint64
	lastRx          atomic.Int64
}

// Metrics returns a point-in-time counter snapshot.
func (c *Client) Metrics() Snapshot {
	return Snapshot{
		LinesTx:         c.linesTx.Load(),
		LinesRx:         c.linesRx.Load(),
		AcquireTimeouts: c.acquireTimeouts.Load(),
		HeartbeatsSent:  c.heartbeatsSent.Load(),
		LastRxUnixNano:  c.lastRx.Load(),
	}
}

// NewClient dials host:port and registers as a WiThrottle throttle.
func NewClient(host string, port uint16, opts ...Option) (*Client, error) {
	cfg := clientOpts{name: "proto"}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.deviceID == "" {
		var b [4]byte
		if _, err := rand.Read(b[:]); err != nil {
			return nil, err
		}
		cfg.deviceID = "proto-" + hex.EncodeToString(b[:])
	}
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, err
	}
	c := &Client{
		conn:     conn,
		r:        bufio.NewReaderSize(conn, maxLineBytes),
		acquired: map[uint16]struct{}{},
		pending:  map[uint16]chan struct{}{},
		speed:    map[uint16]uint8{},
		forward:  map[uint16]bool{},
		fn:       map[uint16]uint32{},
		errCh:    make(chan error, 1),
		closed:   make(chan struct{}),
		hbPeriod: time.Duration(heartbeatSecs) * time.Second,
	}
	for _, line := range []string{"N" + cfg.name, "HU" + cfg.deviceID, "*+"} {
		if err := c.write(line); err != nil {
			_ = conn.Close()
			return nil, err
		}
	}
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		_ = conn.Close()
		return nil, err
	}
	sawVN := false
	for i := 0; i < 16; i++ {
		line, err := c.readLine()
		if err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("withrottle handshake: %w", err)
		}
		c.apply(line)
		if strings.HasPrefix(line, "VN") {
			sawVN = true
		}
		if strings.HasPrefix(line, "HT") {
			break
		}
	}
	if !sawVN {
		_ = conn.Close()
		return nil, fmt.Errorf("withrottle: no VN handshake")
	}
	_ = conn.SetReadDeadline(time.Time{})
	go c.readLoop()
	go c.heartbeatLoop()
	return c, nil
}

func (c *Client) write(line string) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err := c.conn.Write([]byte(line + "\n"))
	if err == nil {
		c.linesTx.Add(1)
	}
	return err
}

func (c *Client) readLine() (string, error) {
	line, err := c.r.ReadString('\n')
	if err != nil {
		return "", err
	}
	c.linesRx.Add(1)
	c.lastRx.Store(time.Now().UnixNano())
	return strings.TrimRight(line, "\r\n"), nil
}

func (c *Client) readLoop() {
	defer close(c.closed)
	for {
		line, err := c.readLine()
		if err != nil {
			select {
			case c.errCh <- err:
			default:
			}
			return
		}
		c.apply(line)
	}
}

func (c *Client) heartbeatLoop() {
	c.mu.Lock()
	d := c.hbPeriod / 2
	c.mu.Unlock()
	if d <= 0 {
		d = 5 * time.Second
	}
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-c.closed:
			return
		case <-t.C:
			if err := c.write("*"); err != nil {
				select {
				case c.errCh <- err:
				default:
				}
				_ = c.conn.Close()
				return
			}
			c.heartbeatsSent.Add(1)
		}
	}
}

func (c *Client) apply(line string) {
	if strings.HasPrefix(line, "*") && len(line) > 1 {
		if n, err := strconv.Atoi(line[1:]); err == nil && n > 0 {
			c.mu.Lock()
			c.hbPeriod = time.Duration(n) * time.Second
			c.mu.Unlock()
		}
		return
	}
	if strings.HasPrefix(line, "HM") && strings.Contains(line, "Steal") {
		c.steal.Store(true)
		c.mu.Lock()
		for _, ch := range c.pending {
			c.signal(ch)
		}
		c.mu.Unlock()
		return
	}
	cmd, ok := ParseM(line)
	if !ok {
		return
	}
	addr, _, ok := ParseLocoKey(cmd.LocoKey)
	if !ok {
		return
	}
	if cmd.Op == MOpAdd || cmd.Op == MOpAction {
		c.noteAcquired(addr)
	}
	if cmd.Op != MOpAction || len(cmd.Properties) == 0 {
		return
	}
	prop := cmd.Properties[0]
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case len(prop) >= 2 && prop[0] == 'V':
		if speed, ok := parseSpeedValue(prop); ok {
			c.speed[addr] = speed
		}
	case len(prop) >= 2 && prop[0] == 'R':
		c.forward[addr] = prop[1] != '0'
	case len(prop) >= 2 && prop[0] == 'F':
		fn, on, ok := parsePress(prop)
		if !ok {
			return
		}
		c.setFnBits(addr, uint8(fn), on)
	case len(prop) >= 2 && prop[0] == 'f':
		fn, on, ok := parseForce(prop)
		if !ok {
			return
		}
		c.setFnBits(addr, uint8(fn), on)
	}
}

func (c *Client) setFnBits(addr uint16, fn uint8, on bool) {
	bits := c.fn[addr]
	if on {
		bits |= 1 << fn
	} else {
		bits &^= 1 << fn
	}
	c.fn[addr] = bits
}

func (c *Client) noteAcquired(addr uint16) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.acquired[addr] = struct{}{}
	if ch, ok := c.pending[addr]; ok {
		c.signal(ch)
		delete(c.pending, addr)
	}
}

func (c *Client) signal(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}

func (c *Client) ensure(addr uint16) error {
	c.mu.Lock()
	if _, ok := c.acquired[addr]; ok {
		c.mu.Unlock()
		return nil
	}
	ch, waiting := c.pending[addr]
	if !waiting {
		ch = make(chan struct{})
		c.pending[addr] = ch
	}
	c.mu.Unlock()
	if !waiting {
		key := LocoKey(addr)
		if err := c.write("M0+" + key + propSep + key); err != nil {
			return err
		}
	}
	select {
	case <-ch:
		if c.steal.Load() {
			return ErrStealRefused
		}
		c.mu.Lock()
		_, ok := c.acquired[addr]
		c.mu.Unlock()
		if !ok {
			return ErrAcquireTimeout
		}
		return nil
	case <-time.After(acquireTimeout):
		c.acquireTimeouts.Add(1)
		c.mu.Lock()
		if c.pending[addr] == ch {
			delete(c.pending, addr)
		}
		c.mu.Unlock()
		return ErrAcquireTimeout
	}
}

func (c *Client) SetSpeed(addr uint16, speed uint8, forward bool, speedSteps uint8) error {
	_ = speedSteps
	if err := c.ensure(addr); err != nil {
		return err
	}
	key := LocoKey(addr)
	dir := 0
	if forward {
		dir = 1
	}
	if speed > 126 {
		speed = 126
	}
	if err := c.write(fmt.Sprintf("M0A%s%sR%d", key, propSep, dir)); err != nil {
		return err
	}
	if err := c.write(fmt.Sprintf("M0A%s%sV%d", key, propSep, speed)); err != nil {
		return err
	}
	c.mu.Lock()
	c.speed[addr] = speed
	c.forward[addr] = forward
	c.mu.Unlock()
	return nil
}

func (c *Client) GetSpeed(addr uint16) (uint8, bool, error) {
	if err := c.ensure(addr); err != nil {
		return 0, false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	speed, ok := c.speed[addr]
	forward, fok := c.forward[addr]
	if !ok {
		speed = 0
	}
	if !fok {
		forward = true
	}
	return speed, forward, nil
}

// EmergencyStop sends M0A«key»<;>X (WiThrottle per-loco e-stop).
func (c *Client) EmergencyStop(addr uint16, forward bool) error {
	if err := c.ensure(addr); err != nil {
		return err
	}
	key := LocoKey(addr)
	if err := c.write(fmt.Sprintf("M0A%s%sX", key, propSep)); err != nil {
		return err
	}
	c.mu.Lock()
	c.speed[addr] = 1
	c.forward[addr] = forward
	c.mu.Unlock()
	return nil
}

func (c *Client) SendFn(addr uint16, num uint8, toggle bool) error {
	if err := c.ensure(addr); err != nil {
		return err
	}
	on := true
	c.mu.Lock()
	bits := c.fn[addr]
	if toggle {
		on = bits&(1<<uint(num)) == 0
	}
	c.mu.Unlock()
	state := 0
	if on {
		state = 1
	}
	key := LocoKey(addr)
	if err := c.write(fmt.Sprintf("M0A%s%sf%d%d", key, propSep, state, int(num))); err != nil {
		return err
	}
	c.mu.Lock()
	c.setFnBits(addr, num, on)
	c.mu.Unlock()
	return nil
}

func (c *Client) ListFunctions(addr uint16) ([]int, error) {
	if err := c.ensure(addr); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	bits := c.fn[addr]
	var out []int
	for i := 0; i <= maxFn; i++ {
		if bits&(1<<uint(i)) != 0 {
			out = append(out, i)
		}
	}
	return out, nil
}

func (c *Client) ReadCV() (int, error) {
	return 0, ErrUnsupported
}

func (c *Client) WriteCV() error {
	return ErrUnsupported
}

func (c *Client) SetTrackPower(on bool) error {
	bit := "0"
	if on {
		bit = "1"
	}
	return c.write("PPA" + bit)
}

func (c *Client) CleanUp() error {
	_ = c.write("Q")
	return c.conn.Close()
}
