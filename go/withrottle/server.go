package withrottle

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"sync"

	"github.com/dcc-bigfred/proto/go/drive"
)

// DefaultPort is the de-facto WiThrottle TCP port (JMRI / LNWI / RB1110).
const DefaultPort uint16 = 12090

const (
	serverName     = "proto"
	heartbeatSecs  = 10
	maxLineBytes   = 8192
	rosterEmpty    = "RL0"
	protocolVer    = "VN2.0"
)

// Server is an inbound WiThrottle TCP listener.
type Server struct {
	host drive.DriveHost
	ln   net.Listener

	mu      sync.Mutex
	conns   map[net.Conn]*session
	trackOn bool
}

type session struct {
	conn     net.Conn
	id       drive.ClientID
	device   string
	name     string
	throttle byte
	locos    map[uint16]struct{}
	forward  map[uint16]bool
	speed    map[uint16]uint8
}

// Listen binds TCP and serves until Close. bind may be "127.0.0.1:0".
func Listen(bind string, host drive.DriveHost) (*Server, error) {
	if bind == "" {
		bind = fmt.Sprintf(":%d", DefaultPort)
	}
	if host == nil {
		host = nopHost{}
	}
	ln, err := net.Listen("tcp", bind)
	if err != nil {
		return nil, err
	}
	s := &Server{
		host:    host,
		ln:      ln,
		conns:   make(map[net.Conn]*session),
		trackOn: true,
	}
	go s.acceptLoop()
	return s, nil
}

// Addr is the bound TCP address.
func (s *Server) Addr() net.Addr { return s.ln.Addr() }

// Close stops the listener and connected sessions.
func (s *Server) Close() error {
	err := s.ln.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.conns {
		_ = c.Close()
	}
	return err
}

// NotifyLocoState pushes M…A V/R/F lines to sessions that acquired addr.
func (s *Server) NotifyLocoState(st drive.LocoState) {
	view := locoView{Speed: st.Speed, Forward: st.Forward, Functions: st.Functions}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sess := range s.conns {
		if _, ok := sess.locos[st.Addr]; !ok {
			continue
		}
		tid := sess.throttle
		if tid == 0 {
			tid = '0'
		}
		for _, line := range buildNotify(tid, st.Addr, view) {
			writeLine(sess.conn, line)
		}
	}
}

// NotifyTrackPower broadcasts PPA0 / PPA1.
func (s *Server) NotifyTrackPower(on bool) {
	s.mu.Lock()
	s.trackOn = on
	line := "PPA0"
	if on {
		line = "PPA1"
	}
	for _, sess := range s.conns {
		writeLine(sess.conn, line)
	}
	s.mu.Unlock()
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
		conn:     conn,
		id:       drive.ClientID(conn.RemoteAddr().String()),
		throttle: '0',
		locos:    map[uint16]struct{}{},
		forward:  map[uint16]bool{},
		speed:    map[uint16]uint8{},
	}
	s.mu.Lock()
	s.conns[conn] = sess
	s.mu.Unlock()
	defer s.drop(sess)

	r := bufio.NewReaderSize(conn, maxLineBytes)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		s.handle(sess, strings.TrimRight(line, "\r\n"))
	}
}

func (s *Server) drop(sess *session) {
	s.mu.Lock()
	delete(s.conns, sess.conn)
	locos := make([]uint16, 0, len(sess.locos))
	for addr := range sess.locos {
		locos = append(locos, addr)
	}
	id := sess.id
	s.mu.Unlock()
	for _, addr := range locos {
		s.host.Release(id, addr)
	}
}

func (s *Server) handle(sess *session, line string) {
	if line == "" {
		return
	}
	switch {
	case strings.HasPrefix(line, "HU"):
		sess.device = strings.TrimSpace(line[2:])
		if sess.device != "" {
			sess.id = drive.ClientID("withrottle:" + sess.device)
		}
		s.sendBurst(sess)
	case strings.HasPrefix(line, "N"):
		sess.name = strings.TrimSpace(line[1:])
	case line == "Q":
		_ = sess.conn.Close()
	case line == "*" || line == "*+" || line == "*-":
		return
	case strings.HasPrefix(line, "PPA"):
		on := len(line) > 3 && line[3] != '0'
		_ = s.host.SetTrackPower(sess.id, on)
		s.NotifyTrackPower(on)
	case strings.HasPrefix(line, "M"):
		s.handleM(sess, line)
	}
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
		fmt.Sprintf("*%d", heartbeatSecs),
		ppa,
		rosterEmpty,
		"HT" + serverName,
	} {
		writeLine(sess.conn, line)
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
			writeLine(sess.conn, "HMInvalid acquire address")
			return
		}
		s.mu.Lock()
		sess.locos[addr] = struct{}{}
		s.mu.Unlock()
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
			writeLine(sess.conn, l)
		}
	case MOpRemove:
		var released []uint16
		if cmd.LocoKey == "*" {
			s.mu.Lock()
			for addr := range sess.locos {
				released = append(released, addr)
			}
			sess.locos = map[uint16]struct{}{}
			s.mu.Unlock()
			writeLine(sess.conn, "M"+string(cmd.ThrottleID)+"-*"+propSep+"r")
		} else if addr, _, ok := ParseLocoKey(cmd.LocoKey); ok {
			s.mu.Lock()
			delete(sess.locos, addr)
			s.mu.Unlock()
			released = []uint16{addr}
			writeLine(sess.conn, buildReleaseLine(cmd.ThrottleID, cmd.LocoKey))
		}
		for _, addr := range released {
			s.host.Release(sess.id, addr)
		}
	case MOpSteal:
		writeLine(sess.conn, "HMSteal not supported")
	case MOpAction:
		s.handleAction(sess, cmd)
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
	case len(prop) >= 2 && (prop[0] == 'F' || prop[0] == 'f'):
		fn, on, _, ok := parseFunctionAction(prop)
		if !ok {
			return
		}
		for _, addr := range addrs {
			_ = s.host.SetFunction(sess.id, addr, uint8(fn), on)
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

func writeLine(conn net.Conn, line string) {
	_, _ = conn.Write([]byte(line + "\n"))
}

type nopHost struct{}

func (nopHost) SetSpeed(drive.ClientID, uint16, uint8, bool, uint8) error { return nil }
func (nopHost) SetFunction(drive.ClientID, uint16, uint8, bool) error    { return nil }
func (nopHost) LocoState(addr uint16) (drive.LocoState, error) {
	return drive.LocoState{Addr: addr, Steps: 128, Forward: true}, nil
}
func (nopHost) SetTrackPower(drive.ClientID, bool) error { return nil }
func (nopHost) Release(drive.ClientID, uint16)           {}
