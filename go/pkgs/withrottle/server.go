package withrottle

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dcc-bigfred/proto/go/pkgs/drive"
)

// DefaultPort is the de-facto WiThrottle TCP port (JMRI / LNWI / RB1110).
const DefaultPort uint16 = 12090

const (
	serverName    = "proto"
	heartbeatSecs = 10
	maxLineBytes  = 8192
	rosterEmpty   = "RL0"
	protocolVer   = "VN2.0"
	writeTimeout  = 5 * time.Second
	loopBackoff   = 50 * time.Millisecond
	maxLoopErrors = 32
)

// Server is an inbound WiThrottle TCP listener.
type Server struct {
	host drive.DriveHost
	cfg  config
	ln   net.Listener
	done chan struct{}

	closeOnce sync.Once
	loops     sync.WaitGroup
	sessWg    sync.WaitGroup

	mu      sync.Mutex
	closed  bool
	conns   map[net.Conn]*session
	byID    map[drive.ClientID]*session
	trackOn bool
}

type session struct {
	conn net.Conn
	wmu  sync.Mutex // serializes writes; never held together with Server.mu

	id        drive.ClientID
	device    string
	name      string
	throttle  byte
	locos     map[byte]map[uint16]struct{} // per MultiThrottle id (M0/M1/…)
	forward   map[uint16]bool
	speed     map[uint16]uint8
	fnBits    map[uint16]uint32
	momentary map[uint16]map[uint8]bool

	burstSent bool
	connected bool // OnConnect delivered for the current id
	lastRx    atomic.Int64
	hbOn      atomic.Bool
}

// Listen binds TCP and serves until Close. bind may be "127.0.0.1:0".
func Listen(bind string, host drive.DriveHost, opts ...ServerOption) (*Server, error) {
	if bind == "" {
		bind = fmt.Sprintf(":%d", DefaultPort)
	}
	if host == nil {
		host = nopHost{}
	}
	cfg := defaultConfig()
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	ln, err := net.Listen("tcp", bind)
	if err != nil {
		return nil, err
	}
	s := &Server{
		host:    host,
		cfg:     cfg,
		ln:      ln,
		done:    make(chan struct{}),
		conns:   make(map[net.Conn]*session),
		byID:    make(map[drive.ClientID]*session),
		trackOn: cfg.trackOn,
	}
	s.loops.Add(1)
	go func() {
		defer s.loops.Done()
		s.acceptLoop()
	}()
	if cfg.deadman {
		s.loops.Add(1)
		go func() {
			defer s.loops.Done()
			s.hbLoop()
		}()
	}
	return s, nil
}

// Addr is the bound TCP address.
func (s *Server) Addr() net.Addr { return s.ln.Addr() }

// Close stops the listener and connected sessions.
func (s *Server) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		close(s.done)
		err = s.ln.Close()
		s.mu.Lock()
		conns := make([]net.Conn, 0, len(s.conns))
		for c := range s.conns {
			conns = append(conns, c)
		}
		s.mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
		s.sessWg.Wait()
		s.loops.Wait()
	})
	return err
}

// ResendBurst sends VN/heartbeat/PPA/RL/HT again (after pairing the roster changes).
func (s *Server) ResendBurst(client drive.ClientID) {
	if sess := s.sessionByID(client); sess != nil {
		s.sendBurst(sess)
	}
}

// SendTo writes a raw line to one client.
func (s *Server) SendTo(client drive.ClientID, line string) error {
	sess := s.sessionByID(client)
	if sess == nil {
		return fmt.Errorf("withrottle: no session %s", client)
	}
	sess.write(line)
	return nil
}

// SendRoster writes only the RL line for one client.
func (s *Server) SendRoster(client drive.ClientID) {
	if sess := s.sessionByID(client); sess != nil {
		sess.write(s.rosterLine(client))
	}
}

// Disconnect closes the TCP connection for client.
func (s *Server) Disconnect(client drive.ClientID) {
	if sess := s.sessionByID(client); sess != nil {
		_ = sess.conn.Close()
	}
}

// HoldersOf returns clients that currently hold addr.
func (s *Server) HoldersOf(addr uint16) []drive.ClientID {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]drive.ClientID, 0)
	for _, sess := range s.conns {
		if sessionHoldsAddr(sess.locos, addr) {
			out = append(out, sess.id)
		}
	}
	return out
}

// SetTrackOn updates the advertised PPA state for subsequent bursts.
func (s *Server) SetTrackOn(on bool) {
	s.mu.Lock()
	s.trackOn = on
	s.mu.Unlock()
}

func (s *Server) sessionByID(id drive.ClientID) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byID[id]
}

// HasSession reports whether a live TCP session is registered for client.
// Consumers use this to ignore OnDisconnect from an HU takeover: the old
// connection's drop runs after the replacement is already in byID.
func (s *Server) HasSession(client drive.ClientID) bool {
	return s.sessionByID(client) != nil
}

// NotifyLocoState pushes M…A V/R/F lines to sessions that acquired addr.
func (s *Server) NotifyLocoState(st drive.LocoState) {
	s.notifyLocoState(st, "")
}

// NotifyLocoStateExcept is NotifyLocoState skipping the commanding handset.
func (s *Server) NotifyLocoStateExcept(st drive.LocoState, origin drive.ClientID) {
	s.notifyLocoState(st, origin)
}

func (s *Server) notifyLocoState(st drive.LocoState, origin drive.ClientID) {
	view := locoView{Speed: st.Speed, Forward: st.Forward, Functions: st.Functions}
	type job struct {
		sess  *session
		lines []string
	}
	s.mu.Lock()
	jobs := make([]job, 0, len(s.conns))
	for _, sess := range s.conns {
		if origin != "" && sess.id == origin {
			continue
		}
		tid, ok := throttleHolding(sess.locos, sess.throttle, st.Addr)
		if !ok {
			continue
		}
		jobs = append(jobs, job{sess: sess, lines: buildNotify(tid, st.Addr, view)})
	}
	s.mu.Unlock()
	for _, j := range jobs {
		j := j
		go j.sess.writeAll(j.lines...)
	}
}

// NotifyTrackPower broadcasts PPA0 / PPA1.
func (s *Server) NotifyTrackPower(on bool) {
	line := "PPA0"
	if on {
		line = "PPA1"
	}
	s.mu.Lock()
	s.trackOn = on
	targets := make([]*session, 0, len(s.conns))
	for _, sess := range s.conns {
		targets = append(targets, sess)
	}
	s.mu.Unlock()
	for _, sess := range targets {
		sess := sess
		go sess.write(line)
	}
}

func (s *Server) acceptLoop() {
	consecutive := 0
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			if s.isClosed() || isClosedNetErr(err) {
				return
			}
			s.reportError(err)
			consecutive++
			if consecutive >= maxLoopErrors {
				return
			}
			time.Sleep(loopBackoff)
			continue
		}
		consecutive = 0
		s.sessWg.Add(1)
		go s.serve(conn)
	}
}

func (s *Server) serve(conn net.Conn) {
	defer s.sessWg.Done()
	defer conn.Close()
	sess := &session{
		conn:      conn,
		id:        drive.ClientID(conn.RemoteAddr().String()),
		throttle:  '0',
		locos:     map[byte]map[uint16]struct{}{},
		forward:   map[uint16]bool{},
		speed:     map[uint16]uint8{},
		fnBits:    map[uint16]uint32{},
		momentary: map[uint16]map[uint8]bool{},
	}
	sess.touch()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.conns[conn] = sess
	s.byID[sess.id] = sess
	s.mu.Unlock()
	defer s.drop(sess)

	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 512), maxLineBytes)
	for {
		if s.cfg.readTimeout > 0 {
			_ = conn.SetReadDeadline(time.Now().Add(s.cfg.readTimeout))
		}
		if !sc.Scan() {
			return
		}
		sess.touch()
		s.handleLine(sess, strings.TrimRight(sc.Text(), "\r"))
	}
}

// handleLine contains a panic raised while processing one line (typically
// inside a host callback) so a single bad line drops neither the session
// nor the process.
func (s *Server) handleLine(sess *session, line string) {
	defer func() { _ = recover() }()
	s.handle(sess, line)
}

func (s *Server) hbLoop() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
			s.enforceHeartbeat()
		}
	}
}

func (s *Server) enforceHeartbeat() {
	secs := s.cfg.heartbeatSecs
	if secs <= 0 {
		secs = heartbeatSecs
	}
	deadline := time.Now().Add(-2 * time.Duration(secs*float64(time.Second)))
	s.mu.Lock()
	var dead []*session
	for _, sess := range s.conns {
		if !sess.hbOn.Load() {
			continue
		}
		if time.Unix(0, sess.lastRx.Load()).Before(deadline) {
			dead = append(dead, sess)
		}
	}
	s.mu.Unlock()
	for _, sess := range dead {
		s.deadman(sess)
		_ = sess.conn.Close()
	}
}

func (s *Server) deadman(sess *session) {
	s.mu.Lock()
	locos := allSessionLocos(sess.locos)
	id := sess.id
	forward := make(map[uint16]bool, len(sess.forward))
	for k, v := range sess.forward {
		forward[k] = v
	}
	s.mu.Unlock()
	for _, addr := range locos {
		fwd := true
		if f, ok := forward[addr]; ok {
			fwd = f
		}
		_ = s.host.SetSpeed(id, addr, 0, fwd, 128)
		s.host.Release(id, addr)
	}
}

func (s *Server) drop(sess *session) {
	s.mu.Lock()
	delete(s.conns, sess.conn)
	// HU takeover installs the new session in byID before closing the
	// old TCP. Skip OnDisconnect/Release so the consumer does not evict
	// the live ClientID (and close the replacement connection).
	replaced := s.byID[sess.id] != sess
	if !replaced {
		delete(s.byID, sess.id)
	}
	locos := allSessionLocos(sess.locos)
	id := sess.id
	s.mu.Unlock()
	if replaced {
		return
	}
	if h, ok := s.host.(drive.SessionHooks); ok {
		h.OnDisconnect(id)
	}
	for _, addr := range locos {
		s.host.Release(id, addr)
	}
}

func (s *Server) handle(sess *session, line string) {
	if line == "" {
		return
	}
	id := s.sessID(sess)
	if sess.device != "" {
		if h, ok := s.host.(drive.SessionHooks); ok {
			h.OnActivity(id)
		}
	}
	switch {
	case strings.HasPrefix(line, "HU"):
		s.handleHU(sess, strings.TrimSpace(line[2:]))
	case strings.HasPrefix(line, "N"):
		s.handleN(sess, strings.TrimSpace(line[1:]))
	case line == "Q":
		if h, ok := s.host.(drive.SessionHooks); ok {
			h.OnQuit(id)
		}
		_ = sess.conn.Close()
	case line == "*":
		return
	case line == "*+":
		sess.hbOn.Store(true)
		if h, ok := s.host.(drive.HeartbeatHook); ok {
			h.OnHeartbeatMonitor(id, true)
		}
	case line == "*-":
		sess.hbOn.Store(false)
		if h, ok := s.host.(drive.HeartbeatHook); ok {
			h.OnHeartbeatMonitor(id, false)
		}
	case strings.HasPrefix(line, "PPA"):
		on := len(line) > 3 && line[3] != '0'
		if g, ok := s.host.(drive.TrackPowerGate); ok && g.TrackPower(id, on) {
			return
		}
		_ = s.host.SetTrackPower(id, on)
		s.NotifyTrackPower(on)
	case strings.HasPrefix(line, "M"):
		s.handleM(sess, line)
	}
}

func (s *Server) handleHU(sess *session, device string) {
	s.mu.Lock()
	sess.device = device
	oldID := sess.id
	if device != "" {
		sess.id = drive.ClientID("withrottle:" + device)
	}
	newID := sess.id
	if oldID != newID {
		if s.byID[oldID] == sess {
			delete(s.byID, oldID)
		}
	}
	var prev *session
	if p, ok := s.byID[newID]; ok && p != sess {
		prev = p
	}
	s.byID[newID] = sess
	s.mu.Unlock()
	if prev != nil {
		_ = prev.conn.Close()
	}
	// A repeated HU with the same device on the same TCP session is not a
	// new connection; consumers treat OnConnect for an already known client
	// as a takeover and reset per-connection state.
	if oldID != newID || !sess.connected {
		sess.connected = true
		if h, ok := s.host.(drive.SessionHooks); ok {
			h.OnConnect(newID, device)
		}
	}
	if !sess.burstSent {
		s.sendBurst(sess)
		sess.burstSent = true
	}
}

func (s *Server) handleN(sess *session, name string) {
	sess.name = name
	id := s.sessID(sess)
	if h, ok := s.host.(drive.NHook); ok {
		if h.OnN(id, name) {
			return
		}
	}
	if sess.burstSent {
		sess.write(s.heartbeatLine())
		return
	}
	s.sendBurst(sess)
	sess.burstSent = true
}

func (s *Server) heartbeatLine() string {
	return fmt.Sprintf("*%g", s.cfg.heartbeatSecs)
}

func (s *Server) sendBurst(sess *session) {
	s.mu.Lock()
	on := s.trackOn
	s.mu.Unlock()
	ppa := "PPA0"
	if on {
		ppa = "PPA1"
	}
	for _, line := range []string{
		protocolVer,
		s.heartbeatLine(),
		ppa,
		s.rosterLine(s.sessID(sess)),
		"HT" + s.cfg.serverName,
	} {
		sess.write(line)
	}
}

func (s *Server) handleM(sess *session, line string) {
	cmd, ok := ParseM(line)
	if !ok {
		return
	}
	s.mu.Lock()
	sess.throttle = cmd.ThrottleID
	id := sess.id
	s.mu.Unlock()
	switch cmd.Op {
	case MOpAdd:
		addr, ok := parseAcquireAddr(cmd.LocoKey, cmd.Properties)
		if !ok {
			sess.write("HMInvalid acquire address")
			return
		}
		proceed := true
		var custom []string
		if g, ok := s.host.(drive.AcquireGate); ok {
			proceed, custom = g.Acquire(id, cmd.ThrottleID, addr)
		}
		hold := proceed || acquireHolds(custom)
		if hold {
			s.mu.Lock()
			holdLoco(sess.locos, cmd.ThrottleID, addr)
			s.mu.Unlock()
		}
		if !proceed {
			for _, l := range custom {
				sess.write(l)
			}
			return
		}
		if len(custom) > 0 {
			for _, l := range custom {
				sess.write(l)
			}
		} else {
			st, err := s.host.LocoState(addr)
			if err != nil {
				st = drive.LocoState{Addr: addr, Steps: 128, Forward: true}
			}
			if st.Addr == 0 {
				st.Addr = addr
			}
			for _, l := range buildAcquireReply(cmd.ThrottleID, addr, locoView{
				Speed: st.Speed, Forward: st.Forward, Functions: st.Functions,
			}) {
				sess.write(l)
			}
			if s.cfg.labels != nil {
				if line := FormatLabelLine(cmd.ThrottleID, LocoKey(addr), s.cfg.labels.Labels(id, addr)); line != "" {
					sess.write(line)
				}
			}
		}
		if sub, ok := s.host.(drive.Subscriber); ok {
			if err := sub.Subscribe(id, addr); err != nil {
				s.mu.Lock()
				unholdLoco(sess.locos, cmd.ThrottleID, addr)
				s.mu.Unlock()
				sess.write(buildReleaseLine(cmd.ThrottleID, LocoKey(addr)))
				if err.Error() != "" {
					sess.write("HM" + truncateHM(err.Error()))
				}
				return
			}
		}
	case MOpRemove:
		s.handleRemove(sess, cmd)
	case MOpSteal:
		sess.write("HMSteal not supported")
	case MOpLabels:
		s.handleLabels(sess, cmd)
	case MOpAction:
		s.handleAction(sess, cmd)
	}
}

func acquireHolds(custom []string) bool {
	for _, l := range custom {
		if strings.Contains(l, "+") && strings.HasPrefix(l, "M") && !strings.HasPrefix(l, "HM") {
			return true
		}
	}
	return false
}

func truncateHM(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	runes := []rune(s)
	if len(runes) > 64 {
		return string(runes[:64])
	}
	return s
}

func (s *Server) handleLabels(sess *session, cmd MCommand) {
	addr, _, ok := ParseLocoKey(cmd.LocoKey)
	if !ok || s.cfg.labels == nil {
		return
	}
	line := FormatLabelLine(cmd.ThrottleID, cmd.LocoKey, s.cfg.labels.Labels(s.sessID(sess), addr))
	if line != "" {
		sess.write(line)
	}
}

func (s *Server) handleRemove(sess *session, cmd MCommand) {
	var released []uint16
	id := s.sessID(sess)
	if cmd.LocoKey == "*" {
		s.mu.Lock()
		held := sess.locos[cmd.ThrottleID]
		for addr := range held {
			released = append(released, addr)
		}
		delete(sess.locos, cmd.ThrottleID)
		s.mu.Unlock()
		if g, ok := s.host.(drive.ReleaseGate); ok {
			addr := uint16(0)
			if len(released) > 0 {
				addr = released[0]
			}
			if g.GateRelease(id, cmd.ThrottleID, cmd.LocoKey, addr) {
				return
			}
		}
		sess.write("M" + string(cmd.ThrottleID) + "-*" + propSep + "r")
	} else if addr, _, ok := ParseLocoKey(cmd.LocoKey); ok {
		s.mu.Lock()
		unholdLoco(sess.locos, cmd.ThrottleID, addr)
		s.mu.Unlock()
		released = []uint16{addr}
		if g, ok := s.host.(drive.ReleaseGate); ok {
			if g.GateRelease(id, cmd.ThrottleID, cmd.LocoKey, addr) {
				return
			}
		}
		sess.write(buildReleaseLine(cmd.ThrottleID, cmd.LocoKey))
	}
	for _, addr := range released {
		s.host.Release(id, addr)
	}
}

func (s *Server) handleAction(sess *session, cmd MCommand) {
	if len(cmd.Properties) == 0 {
		return
	}
	prop := cmd.Properties[0]
	id := s.sessID(sess)
	addrs := s.addrs(sess, cmd.ThrottleID, cmd.LocoKey)
	if g, ok := s.host.(drive.ActionGate); ok {
		if len(addrs) == 0 {
			if cmd.LocoKey != "*" {
				if addr, _, parsed := ParseLocoKey(cmd.LocoKey); parsed {
					if g.Action(id, cmd.ThrottleID, cmd.LocoKey, addr, prop) {
						return
					}
				}
			}
			return
		}
		if cmd.LocoKey == "*" {
			// One wire command, one gate call: the consumer expands "*" over
			// its own per-throttle view. Calling per held address would
			// replay the same action len(addrs) times.
			if g.Action(id, cmd.ThrottleID, cmd.LocoKey, addrs[0], prop) {
				return
			}
		} else {
			allHandled := true
			for _, addr := range addrs {
				if !g.Action(id, cmd.ThrottleID, cmd.LocoKey, addr, prop) {
					allHandled = false
					break
				}
			}
			if allHandled {
				return
			}
		}
	}
	if len(addrs) == 0 {
		return
	}
	switch {
	case len(prop) >= 2 && prop[0] == 'V':
		speed, ok := parseSpeedValue(prop)
		if !ok {
			return
		}
		for _, addr := range addrs {
			forward := true
			s.mu.Lock()
			if f, ok := sess.forward[addr]; ok {
				forward = f
			}
			sess.speed[addr] = speed
			s.mu.Unlock()
			_ = s.host.SetSpeed(id, addr, speed, forward, 128)
		}
	case len(prop) >= 2 && prop[0] == 'R':
		forward := prop[1] != '0'
		for _, addr := range addrs {
			s.mu.Lock()
			sess.forward[addr] = forward
			speed, ok := sess.speed[addr]
			s.mu.Unlock()
			if !ok {
				if st, err := s.host.LocoState(addr); err == nil {
					speed = st.Speed
				}
			}
			_ = s.host.SetSpeed(id, addr, speed, forward, 128)
		}
	case len(prop) >= 2 && prop[0] == 'f':
		fn, on, ok := parseForce(prop)
		if !ok {
			return
		}
		for _, addr := range addrs {
			s.setFn(sess, addr, uint8(fn), on)
		}
	case len(prop) >= 2 && prop[0] == 'F':
		fn, pressed, ok := parsePress(prop)
		if !ok {
			return
		}
		for _, addr := range addrs {
			switch {
			case s.isMomentary(sess, addr, uint8(fn)):
				s.setFn(sess, addr, uint8(fn), pressed)
			case pressed:
				s.setFn(sess, addr, uint8(fn), !s.fnState(sess, addr, uint8(fn)))
			}
		}
	case len(prop) >= 2 && prop[0] == 'm':
		fn, mom, ok := parseMode(prop)
		if !ok {
			return
		}
		for _, addr := range addrs {
			s.setMomentary(sess, addr, uint8(fn), mom)
		}
	case prop == "X":
		for _, addr := range addrs {
			forward := true
			s.mu.Lock()
			if f, ok := sess.forward[addr]; ok {
				forward = f
			}
			sess.speed[addr] = 1
			s.mu.Unlock()
			_ = s.host.SetSpeed(id, addr, 1, forward, 128)
		}
	case prop == "I":
		for _, addr := range addrs {
			forward := true
			s.mu.Lock()
			if f, ok := sess.forward[addr]; ok {
				forward = f
			}
			sess.speed[addr] = 0
			s.mu.Unlock()
			_ = s.host.SetSpeed(id, addr, 0, forward, 128)
		}
	}
}

func (s *Server) setFn(sess *session, addr uint16, fn uint8, on bool) {
	if s.fnState(sess, addr, fn) == on {
		return
	}
	s.mu.Lock()
	id := sess.id
	tid := sess.throttle
	s.mu.Unlock()
	_ = s.host.SetFunction(id, addr, fn, on)
	s.mu.Lock()
	bits := sess.fnBits[addr]
	if on {
		bits |= 1 << fn
	} else {
		bits &^= 1 << fn
	}
	sess.fnBits[addr] = bits
	s.mu.Unlock()
	if tid == 0 {
		tid = '0'
	}
	bit := 0
	if on {
		bit = 1
	}
	sess.write("M" + string(tid) + "A" + LocoKey(addr) + propSep + "F" + fmt.Sprintf("%d%d", bit, fn))
}

func (s *Server) fnState(sess *session, addr uint16, fn uint8) bool {
	if st, err := s.host.LocoState(addr); err == nil {
		return st.Functions&(1<<fn) != 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return sess.fnBits[addr]&(1<<fn) != 0
}

func (s *Server) isMomentary(sess *session, addr uint16, fn uint8) bool {
	s.mu.Lock()
	if m, ok := sess.momentary[addr]; ok {
		if v, ok := m[fn]; ok {
			s.mu.Unlock()
			return v
		}
	}
	s.mu.Unlock()
	if fm, ok := s.host.(drive.FunctionModer); ok {
		return fm.Momentary(addr, fn)
	}
	return fn == 2
}

func (s *Server) setMomentary(sess *session, addr uint16, fn uint8, mom bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess.momentary[addr] == nil {
		sess.momentary[addr] = map[uint8]bool{}
	}
	sess.momentary[addr][fn] = mom
}

func (s *Server) addrs(sess *session, throttleID byte, key string) []uint16 {
	s.mu.Lock()
	defer s.mu.Unlock()
	held := sess.locos[throttleID]
	if key == "*" {
		out := make([]uint16, 0, len(held))
		for addr := range held {
			out = append(out, addr)
		}
		return out
	}
	addr, _, ok := ParseLocoKey(key)
	if !ok {
		return nil
	}
	if _, ok := held[addr]; !ok {
		return nil
	}
	return []uint16{addr}
}

func (sess *session) touch() {
	sess.lastRx.Store(time.Now().UnixNano())
}

func (sess *session) write(line string) {
	sess.writeAll(line)
}

func (sess *session) writeAll(lines ...string) {
	if len(lines) == 0 {
		return
	}
	sess.wmu.Lock()
	defer sess.wmu.Unlock()
	for _, line := range lines {
		_ = sess.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
		if _, err := sess.conn.Write([]byte(line + "\n")); err != nil {
			_ = sess.conn.Close()
			return
		}
	}
}

func (s *Server) sessID(sess *session) drive.ClientID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return sess.id
}

func (s *Server) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *Server) reportError(err error) {
	if s.cfg.onError != nil {
		s.cfg.onError(err)
	}
}

func isClosedNetErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, net.ErrClosed) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "closed network connection") || strings.Contains(msg, "use of closed")
}

func holdLoco(locos map[byte]map[uint16]struct{}, tid byte, addr uint16) {
	m := locos[tid]
	if m == nil {
		m = map[uint16]struct{}{}
		locos[tid] = m
	}
	m[addr] = struct{}{}
}

func unholdLoco(locos map[byte]map[uint16]struct{}, tid byte, addr uint16) {
	m := locos[tid]
	if m == nil {
		return
	}
	delete(m, addr)
	if len(m) == 0 {
		delete(locos, tid)
	}
}

func sessionHoldsAddr(locos map[byte]map[uint16]struct{}, addr uint16) bool {
	for _, m := range locos {
		if _, ok := m[addr]; ok {
			return true
		}
	}
	return false
}

func allSessionLocos(locos map[byte]map[uint16]struct{}) []uint16 {
	seen := make(map[uint16]struct{})
	out := make([]uint16, 0)
	for _, m := range locos {
		for addr := range m {
			if _, ok := seen[addr]; ok {
				continue
			}
			seen[addr] = struct{}{}
			out = append(out, addr)
		}
	}
	return out
}

// throttleHolding returns the MultiThrottle id that holds addr. Prefer last.
func throttleHolding(locos map[byte]map[uint16]struct{}, last byte, addr uint16) (byte, bool) {
	if m := locos[last]; m != nil {
		if _, ok := m[addr]; ok {
			if last == 0 {
				return '0', true
			}
			return last, true
		}
	}
	for tid, m := range locos {
		if _, ok := m[addr]; ok {
			if tid == 0 {
				return '0', true
			}
			return tid, true
		}
	}
	return 0, false
}

type nopHost struct{}

func (nopHost) SetSpeed(drive.ClientID, uint16, uint8, bool, uint8) error { return nil }
func (nopHost) SetFunction(drive.ClientID, uint16, uint8, bool) error     { return nil }
func (nopHost) LocoState(addr uint16) (drive.LocoState, error) {
	return drive.LocoState{Addr: addr, Steps: 128, Forward: true}, nil
}
func (nopHost) SetTrackPower(drive.ClientID, bool) error { return nil }
func (nopHost) Release(drive.ClientID, uint16)           {}
