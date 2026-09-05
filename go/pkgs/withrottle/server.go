package withrottle

import (
	"bufio"
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
)

// Server is an inbound WiThrottle TCP listener.
type Server struct {
	host drive.DriveHost
	cfg  config
	ln   net.Listener
	done chan struct{}

	mu      sync.Mutex
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
	locos     map[uint16]struct{}
	forward   map[uint16]bool
	speed     map[uint16]uint8
	fnBits    map[uint16]uint32
	momentary map[uint16]map[uint8]bool

	burstSent bool
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
	go s.acceptLoop()
	if cfg.deadman {
		go s.hbLoop()
	}
	return s, nil
}

// Addr is the bound TCP address.
func (s *Server) Addr() net.Addr { return s.ln.Addr() }

// Close stops the listener and connected sessions.
func (s *Server) Close() error {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	err := s.ln.Close()
	s.mu.Lock()
	conns := make([]net.Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
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
		if _, ok := sess.locos[addr]; ok {
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
		if _, ok := sess.locos[st.Addr]; !ok {
			continue
		}
		tid := sess.throttle
		if tid == 0 {
			tid = '0'
		}
		jobs = append(jobs, job{sess: sess, lines: buildNotify(tid, st.Addr, view)})
	}
	s.mu.Unlock()
	for _, j := range jobs {
		j := j
		go func() {
			for _, line := range j.lines {
				j.sess.write(line)
			}
		}()
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
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.serve(conn)
	}
}

func (s *Server) serve(conn net.Conn) {
	defer conn.Close()
	sess := &session{
		conn:      conn,
		id:        drive.ClientID(conn.RemoteAddr().String()),
		throttle:  '0',
		locos:     map[uint16]struct{}{},
		forward:   map[uint16]bool{},
		speed:     map[uint16]uint8{},
		fnBits:    map[uint16]uint32{},
		momentary: map[uint16]map[uint8]bool{},
	}
	sess.touch()
	s.mu.Lock()
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
		s.handle(sess, strings.TrimRight(sc.Text(), "\r"))
	}
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
	locos := make([]uint16, 0, len(sess.locos))
	for addr := range sess.locos {
		locos = append(locos, addr)
	}
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
	if s.byID[sess.id] == sess {
		delete(s.byID, sess.id)
	}
	locos := make([]uint16, 0, len(sess.locos))
	for addr := range sess.locos {
		locos = append(locos, addr)
	}
	id := sess.id
	s.mu.Unlock()
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
	if sess.device != "" {
		if h, ok := s.host.(drive.SessionHooks); ok {
			h.OnActivity(sess.id)
		}
	}
	switch {
	case strings.HasPrefix(line, "HU"):
		s.handleHU(sess, strings.TrimSpace(line[2:]))
	case strings.HasPrefix(line, "N"):
		s.handleN(sess, strings.TrimSpace(line[1:]))
	case line == "Q":
		if h, ok := s.host.(drive.SessionHooks); ok {
			h.OnQuit(sess.id)
		}
		_ = sess.conn.Close()
	case line == "*":
		return
	case line == "*+":
		sess.hbOn.Store(true)
	case line == "*-":
		sess.hbOn.Store(false)
	case strings.HasPrefix(line, "PPA"):
		on := len(line) > 3 && line[3] != '0'
		if g, ok := s.host.(drive.TrackPowerGate); ok && g.TrackPower(sess.id, on) {
			return
		}
		_ = s.host.SetTrackPower(sess.id, on)
		s.NotifyTrackPower(on)
	case strings.HasPrefix(line, "M"):
		s.handleM(sess, line)
	}
}

func (s *Server) handleHU(sess *session, device string) {
	sess.device = device
	oldID := sess.id
	if device != "" {
		sess.id = drive.ClientID("withrottle:" + device)
	}
	s.mu.Lock()
	if oldID != sess.id {
		if s.byID[oldID] == sess {
			delete(s.byID, oldID)
		}
	}
	if prev, ok := s.byID[sess.id]; ok && prev != sess {
		s.mu.Unlock()
		_ = prev.conn.Close()
		s.mu.Lock()
	}
	s.byID[sess.id] = sess
	s.mu.Unlock()
	if h, ok := s.host.(drive.SessionHooks); ok {
		h.OnConnect(sess.id, device)
	}
	if !sess.burstSent {
		s.sendBurst(sess)
		sess.burstSent = true
	}
}

func (s *Server) handleN(sess *session, name string) {
	sess.name = name
	if h, ok := s.host.(drive.NHook); ok {
		if h.OnN(sess.id, name) {
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
		s.rosterLine(sess.id),
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
	sess.throttle = cmd.ThrottleID
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
			proceed, custom = g.Acquire(sess.id, addr)
		}
		hold := proceed || acquireHolds(custom)
		if hold {
			s.mu.Lock()
			sess.locos[addr] = struct{}{}
			s.mu.Unlock()
		}
		if !proceed {
			for _, l := range custom {
				sess.write(l)
			}
			return
		}
		if sub, ok := s.host.(drive.Subscriber); ok {
			if err := sub.Subscribe(sess.id, addr); err != nil {
				s.mu.Lock()
				delete(sess.locos, addr)
				s.mu.Unlock()
				sess.write(buildReleaseLine(cmd.ThrottleID, LocoKey(addr)))
				if err.Error() != "" {
					sess.write("HM" + truncateHM(err.Error()))
				}
				return
			}
		}
		if len(custom) > 0 {
			for _, l := range custom {
				sess.write(l)
			}
			return
		}
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
			if line := FormatLabelLine(cmd.ThrottleID, LocoKey(addr), s.cfg.labels.Labels(sess.id, addr)); line != "" {
				sess.write(line)
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
	line := FormatLabelLine(cmd.ThrottleID, cmd.LocoKey, s.cfg.labels.Labels(sess.id, addr))
	if line != "" {
		sess.write(line)
	}
}

func (s *Server) handleRemove(sess *session, cmd MCommand) {
	var released []uint16
	if cmd.LocoKey == "*" {
		s.mu.Lock()
		for addr := range sess.locos {
			released = append(released, addr)
		}
		sess.locos = map[uint16]struct{}{}
		s.mu.Unlock()
		if g, ok := s.host.(drive.ReleaseGate); ok {
			handled := false
			for _, addr := range released {
				if g.GateRelease(sess.id, cmd.ThrottleID, cmd.LocoKey, addr) {
					handled = true
				}
			}
			if handled {
				return
			}
		}
		sess.write("M" + string(cmd.ThrottleID) + "-*" + propSep + "r")
	} else if addr, _, ok := ParseLocoKey(cmd.LocoKey); ok {
		s.mu.Lock()
		delete(sess.locos, addr)
		s.mu.Unlock()
		released = []uint16{addr}
		if g, ok := s.host.(drive.ReleaseGate); ok {
			if g.GateRelease(sess.id, cmd.ThrottleID, cmd.LocoKey, addr) {
				return
			}
		}
		sess.write(buildReleaseLine(cmd.ThrottleID, cmd.LocoKey))
	}
	for _, addr := range released {
		s.host.Release(sess.id, addr)
	}
}

func (s *Server) handleAction(sess *session, cmd MCommand) {
	if len(cmd.Properties) == 0 {
		return
	}
	prop := cmd.Properties[0]
	addrs := s.addrs(sess, cmd.LocoKey)
	if len(addrs) == 0 {
		return
	}
	if g, ok := s.host.(drive.ActionGate); ok {
		allHandled := true
		for _, addr := range addrs {
			if !g.Action(sess.id, cmd.ThrottleID, cmd.LocoKey, addr, prop) {
				allHandled = false
				break
			}
		}
		if allHandled {
			return
		}
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
			_ = s.host.SetSpeed(sess.id, addr, speed, forward, 128)
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
			_ = s.host.SetSpeed(sess.id, addr, speed, forward, 128)
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
			_ = s.host.SetSpeed(sess.id, addr, 1, forward, 128)
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
			_ = s.host.SetSpeed(sess.id, addr, 0, forward, 128)
		}
	}
}

func (s *Server) setFn(sess *session, addr uint16, fn uint8, on bool) {
	if s.fnState(sess, addr, fn) == on {
		return
	}
	_ = s.host.SetFunction(sess.id, addr, fn, on)
	s.mu.Lock()
	bits := sess.fnBits[addr]
	if on {
		bits |= 1 << fn
	} else {
		bits &^= 1 << fn
	}
	sess.fnBits[addr] = bits
	tid := sess.throttle
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

func (s *Server) addrs(sess *session, key string) []uint16 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key == "*" {
		out := make([]uint16, 0, len(sess.locos))
		for addr := range sess.locos {
			out = append(out, addr)
		}
		return out
	}
	addr, _, ok := ParseLocoKey(key)
	if !ok {
		return nil
	}
	if _, held := sess.locos[addr]; !held {
		return nil
	}
	return []uint16{addr}
}

func (sess *session) touch() {
	sess.lastRx.Store(time.Now().UnixNano())
}

func (sess *session) write(line string) {
	sess.wmu.Lock()
	defer sess.wmu.Unlock()
	_ = sess.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	if _, err := sess.conn.Write([]byte(line + "\n")); err != nil {
		_ = sess.conn.Close()
	}
}

type nopHost struct{}

func (nopHost) SetSpeed(drive.ClientID, uint16, uint8, bool, uint8) error { return nil }
func (nopHost) SetFunction(drive.ClientID, uint16, uint8, bool) error     { return nil }
func (nopHost) LocoState(addr uint16) (drive.LocoState, error) {
	return drive.LocoState{Addr: addr, Steps: 128, Forward: true}, nil
}
func (nopHost) SetTrackPower(drive.ClientID, bool) error { return nil }
func (nopHost) Release(drive.ClientID, uint16)           {}
