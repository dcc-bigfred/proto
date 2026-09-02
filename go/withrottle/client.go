package withrottle

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/dcc-bigfred/proto/go/commandstation"
)

// Compile-time check: Client is a Station.
var _ commandstation.Station = (*Client)(nil)
var _ commandstation.TrackPowerController = (*Client)(nil)

// Client is a WiThrottle TCP Station (JMRI / DCC-EX / LNWI / RB1110).
type Client struct {
	conn net.Conn
	r    *bufio.Reader

	mu       sync.Mutex
	acquired map[uint16]struct{}
	speed    map[uint16]uint8
	forward  map[uint16]bool
	fn       map[uint16]uint32
	errCh    chan error
	closed   chan struct{}
}

// NewClient dials host:port and registers as a WiThrottle throttle.
func NewClient(host string, port uint16) (*Client, error) {
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, err
	}
	c := &Client{
		conn:     conn,
		r:        bufio.NewReaderSize(conn, maxLineBytes),
		acquired: map[uint16]struct{}{},
		speed:    map[uint16]uint8{},
		forward:  map[uint16]bool{},
		fn:       map[uint16]uint32{},
		errCh:    make(chan error, 1),
		closed:   make(chan struct{}),
	}
	for _, line := range []string{"Nproto", "HUproto", "*+"} {
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
		if line == protocolVer || strings.HasPrefix(line, "VN") {
			sawVN = true
			break
		}
	}
	if !sawVN {
		_ = conn.Close()
		return nil, fmt.Errorf("withrottle: no VN handshake")
	}
	_ = conn.SetReadDeadline(time.Time{})
	go c.readLoop()
	return c, nil
}

func (c *Client) write(line string) error {
	_, err := c.conn.Write([]byte(line + "\n"))
	return err
}

func (c *Client) readLine() (string, error) {
	line, err := c.r.ReadString('\n')
	if err != nil {
		return "", err
	}
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

func (c *Client) apply(line string) {
	cmd, ok := ParseM(line)
	if !ok || cmd.Op != MOpAction || len(cmd.Properties) == 0 {
		return
	}
	addr, _, ok := ParseLocoKey(cmd.LocoKey)
	if !ok {
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
	case len(prop) >= 2 && (prop[0] == 'F' || prop[0] == 'f'):
		fn, on, _, ok := parseFunctionAction(prop)
		if !ok {
			return
		}
		bits := c.fn[addr]
		if on {
			bits |= 1 << uint(fn)
		} else {
			bits &^= 1 << uint(fn)
		}
		c.fn[addr] = bits
	}
}

func (c *Client) ensure(addr commandstation.LocoAddr) error {
	a := uint16(addr)
	c.mu.Lock()
	_, ok := c.acquired[a]
	c.mu.Unlock()
	if ok {
		return nil
	}
	key := LocoKey(a)
	if err := c.write("M0+" + key + propSep + key); err != nil {
		return err
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		_, have := c.speed[a]
		_, haveFn := c.fn[a]
		c.mu.Unlock()
		if have || haveFn {
			c.mu.Lock()
			c.acquired[a] = struct{}{}
			c.mu.Unlock()
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.mu.Lock()
	c.acquired[a] = struct{}{}
	c.mu.Unlock()
	return nil
}

func (c *Client) SetSpeed(addr commandstation.LocoAddr, speed uint8, forward bool, speedSteps uint8) error {
	_ = speedSteps
	if err := c.ensure(addr); err != nil {
		return err
	}
	key := LocoKey(uint16(addr))
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
	c.speed[uint16(addr)] = speed
	c.forward[uint16(addr)] = forward
	c.mu.Unlock()
	return nil
}

func (c *Client) GetSpeed(addr commandstation.LocoAddr) (uint8, bool, error) {
	if err := c.ensure(addr); err != nil {
		return 0, false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	speed, ok := c.speed[uint16(addr)]
	forward, fok := c.forward[uint16(addr)]
	if !ok {
		speed = 0
	}
	if !fok {
		forward = true
	}
	return speed, forward, nil
}

func (c *Client) SendFn(_ commandstation.Mode, addr commandstation.LocoAddr, num commandstation.FuncNum, toggle bool) error {
	if err := c.ensure(addr); err != nil {
		return err
	}
	on := true
	c.mu.Lock()
	bits := c.fn[uint16(addr)]
	if toggle {
		on = bits&(1<<uint(num)) == 0
	}
	c.mu.Unlock()
	state := 0
	if on {
		state = 1
	}
	key := LocoKey(uint16(addr))
	if err := c.write(fmt.Sprintf("M0A%s%sf%d%d", key, propSep, state, int(num))); err != nil {
		return err
	}
	c.mu.Lock()
	if on {
		c.fn[uint16(addr)] |= 1 << uint(num)
	} else {
		c.fn[uint16(addr)] &^= 1 << uint(num)
	}
	c.mu.Unlock()
	return nil
}

func (c *Client) ListFunctions(addr commandstation.LocoAddr) ([]int, error) {
	if err := c.ensure(addr); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	bits := c.fn[uint16(addr)]
	var out []int
	for i := 0; i <= maxFn; i++ {
		if bits&(1<<uint(i)) != 0 {
			out = append(out, i)
		}
	}
	return out, nil
}

func (c *Client) ReadCV(commandstation.Mode, commandstation.LocoCV, ...commandstation.Option) (int, error) {
	return 0, commandstation.ErrUnsupported
}

func (c *Client) WriteCV(commandstation.Mode, commandstation.LocoCV, ...commandstation.Option) error {
	return commandstation.ErrUnsupported
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
