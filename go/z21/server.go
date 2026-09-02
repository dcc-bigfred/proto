package z21

import (
	"net"
	"sync"

	"github.com/dcc-bigfred/proto/go/drive"
)

// Server is an inbound Z21 LAN UDP listener.
type Server struct {
	host   drive.DriveHost
	serial uint32
	conn   *net.UDPConn
	mu     sync.Mutex
	peers  map[string]*net.UDPAddr
	subs   map[string]map[uint16]struct{} // peer → locos from GET_LOCO_INFO
}

// Listen binds UDP and serves packets until Close. bind may be ":0".
func Listen(bind string, host drive.DriveHost) (*Server, error) {
	if bind == "" {
		bind = ":21105"
	}
	if host == nil {
		host = nopHost{}
	}
	addr, err := net.ResolveUDPAddr("udp", bind)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, err
	}
	s := &Server{
		host:   host,
		serial: 258_000_001,
		conn:   conn,
		peers:  make(map[string]*net.UDPAddr),
		subs:   make(map[string]map[uint16]struct{}),
	}
	go s.readLoop()
	return s, nil
}

// Addr is the bound UDP address.
func (s *Server) Addr() net.Addr { return s.conn.LocalAddr() }

// Close stops the listener.
func (s *Server) Close() error { return s.conn.Close() }

// NotifyLocoState pushes LAN_X_LOCO_INFO to peers that subscribed to addr
// (or to every peer if none have subscribed).
func (s *Server) NotifyLocoState(st drive.LocoState) {
	pkt := BuildLocoInfo(st)
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, ua := range s.peers {
		if locos, ok := s.subs[key]; ok && len(locos) > 0 {
			if _, want := locos[st.Addr]; !want {
				continue
			}
		}
		_, _ = s.conn.WriteToUDP(pkt, ua)
	}
}

// NotifyTrackPower broadcasts LAN_X_BC_TRACK_POWER_*.
func (s *Server) NotifyTrackPower(on bool) {
	db0 := byte(0x00)
	if on {
		db0 = 0x01
	}
	// LAN_X_BC_TRACK_POWER_OFF 0x61 0x00 / ON 0x61 0x01
	pkt := xbus([]byte{0x61, db0})
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ua := range s.peers {
		_, _ = s.conn.WriteToUDP(pkt, ua)
	}
}

func (s *Server) readLoop() {
	buf := make([]byte, 2048)
	for {
		n, from, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		payload := append([]byte(nil), buf[:n]...)
		s.notePeer(from)
		for _, pkt := range SplitDatagram(payload) {
			s.handle(pkt, from)
		}
	}
}

func (s *Server) notePeer(from *net.UDPAddr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.peers[from.String()] = from
}

func (s *Server) handle(pkt []byte, from *net.UDPAddr) {
	if reply, ok := handshakeReply(pkt, s.serial); ok {
		_, _ = s.conn.WriteToUDP(reply, from)
		return
	}
	_, header, ok := PacketHeader(pkt)
	if !ok {
		return
	}
	if header == HeaderLogoff {
		s.mu.Lock()
		delete(s.peers, from.String())
		delete(s.subs, from.String())
		s.mu.Unlock()
		s.host.Release(drive.ClientID(from.String()), 0)
		return
	}
	if header != HeaderXBus {
		return
	}
	id := drive.ClientID(from.String())
	if addr, speed, forward, ok := ParseSetLocoDrive(pkt); ok {
		steps := uint8(128)
		if len(pkt) > 5 {
			switch pkt[5] & 0x0F {
			case 0:
				steps = 14
			case 2:
				steps = 28
			}
		}
		_ = s.host.SetSpeed(id, addr, speed, forward, steps)
		s.echoLoco(from, addr)
		return
	}
	if addr, fn, on, toggle, ok := ParseSetLocoFunction(pkt); ok {
		if toggle {
			st, err := s.host.LocoState(addr)
			if err == nil {
				on = st.Functions&(1<<uint(fn)) == 0
			}
		}
		_ = s.host.SetFunction(id, addr, uint8(fn), on)
		s.echoLoco(from, addr)
		return
	}
	if addr, ok := ParseGetLocoInfo(pkt); ok {
		s.mu.Lock()
		if s.subs[from.String()] == nil {
			s.subs[from.String()] = make(map[uint16]struct{})
		}
		s.subs[from.String()][addr] = struct{}{}
		s.mu.Unlock()
		s.echoLoco(from, addr)
		return
	}
	if on, ok := ParseTrackPower(pkt); ok {
		_ = s.host.SetTrackPower(id, on)
		s.NotifyTrackPower(on)
	}
}

func (s *Server) echoLoco(from *net.UDPAddr, addr uint16) {
	st, err := s.host.LocoState(addr)
	if err != nil {
		st = drive.LocoState{Addr: addr, Steps: 128}
	}
	if st.Addr == 0 {
		st.Addr = addr
	}
	if st.Steps == 0 {
		st.Steps = 128
	}
	_, _ = s.conn.WriteToUDP(BuildLocoInfo(st), from)
}

type nopHost struct{}

func (nopHost) SetSpeed(drive.ClientID, uint16, uint8, bool, uint8) error {
	return nil
}
func (nopHost) SetFunction(drive.ClientID, uint16, uint8, bool) error { return nil }
func (nopHost) LocoState(addr uint16) (drive.LocoState, error) {
	return drive.LocoState{Addr: addr, Steps: 128}, nil
}
func (nopHost) SetTrackPower(drive.ClientID, bool) error { return nil }
func (nopHost) Release(drive.ClientID, uint16)           {}
