package z21

import (
	"net"
	"sync"
	"time"

	"github.com/dcc-bigfred/proto/go/pkgs/drive"
)

const (
	peerTTL   = 60 * time.Second
	peerSweep = 10 * time.Second
)

// Server is an inbound Z21 LAN UDP listener.
type Server struct {
	host   drive.DriveHost
	serial uint32
	conn   *net.UDPConn
	done   chan struct{}

	mu    sync.Mutex
	peers map[string]*peer
}

type peer struct {
	addr     *net.UDPAddr
	flags    uint32
	subs     map[uint16]struct{}
	held     map[uint16]struct{}
	lastSeen time.Time
}

func (p *peer) wantsLoco(addr uint16) bool {
	if p.flags&BcAllLocos != 0 {
		return true
	}
	if p.flags&BcDrivingSwitching != 0 {
		_, ok := p.subs[addr]
		return ok
	}
	return false
}

func (p *peer) wantsPower() bool {
	return p.flags&BcDrivingSwitching != 0
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
		done:   make(chan struct{}),
		peers:  make(map[string]*peer),
	}
	go s.readLoop()
	go s.ttlLoop()
	return s, nil
}

// Addr is the bound UDP address.
func (s *Server) Addr() net.Addr { return s.conn.LocalAddr() }

// Close stops the listener.
func (s *Server) Close() error {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	return s.conn.Close()
}

// NotifyLocoState pushes LAN_X_LOCO_INFO to peers whose broadcast flags match.
func (s *Server) NotifyLocoState(st drive.LocoState) {
	pkt := BuildLocoInfo(st)
	s.mu.Lock()
	targets := make([]*net.UDPAddr, 0, len(s.peers))
	for _, p := range s.peers {
		if p.wantsLoco(st.Addr) {
			targets = append(targets, p.addr)
		}
	}
	s.mu.Unlock()
	for _, ua := range targets {
		_, _ = s.conn.WriteToUDP(pkt, ua)
	}
}

// NotifyTrackPower broadcasts LAN_X_BC_TRACK_POWER_* to peers with driving flags.
func (s *Server) NotifyTrackPower(on bool) {
	db0 := byte(0x00)
	if on {
		db0 = 0x01
	}
	pkt := xbus([]byte{0x61, db0})
	s.mu.Lock()
	targets := make([]*net.UDPAddr, 0, len(s.peers))
	for _, p := range s.peers {
		if p.wantsPower() {
			targets = append(targets, p.addr)
		}
	}
	s.mu.Unlock()
	for _, ua := range targets {
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

func (s *Server) ttlLoop() {
	t := time.NewTicker(peerSweep)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case now := <-t.C:
			s.evictStale(now)
		}
	}
}

func (s *Server) evictStale(now time.Time) {
	cutoff := now.Add(-peerTTL)
	type held struct {
		id   drive.ClientID
		addr uint16
	}
	var release []held
	s.mu.Lock()
	for key, p := range s.peers {
		if p.lastSeen.After(cutoff) {
			continue
		}
		id := drive.ClientID(key)
		for addr := range p.held {
			release = append(release, held{id, addr})
		}
		delete(s.peers, key)
	}
	s.mu.Unlock()
	for _, h := range release {
		s.host.Release(h.id, h.addr)
	}
}

func (s *Server) notePeer(from *net.UDPAddr) *peer {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.peers[from.String()]
	if !ok {
		p = &peer{
			addr: from,
			subs: make(map[uint16]struct{}),
			held: make(map[uint16]struct{}),
		}
		s.peers[from.String()] = p
	}
	p.addr = from
	p.lastSeen = time.Now()
	return p
}

func (s *Server) markHeld(from *net.UDPAddr, addr uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.peers[from.String()]; ok {
		p.held[addr] = struct{}{}
	}
}

func (s *Server) dropPeer(from *net.UDPAddr) {
	key := from.String()
	s.mu.Lock()
	p, ok := s.peers[key]
	if !ok {
		s.mu.Unlock()
		return
	}
	held := make([]uint16, 0, len(p.held))
	for addr := range p.held {
		held = append(held, addr)
	}
	delete(s.peers, key)
	s.mu.Unlock()
	id := drive.ClientID(key)
	for _, addr := range held {
		s.host.Release(id, addr)
	}
}

func (s *Server) handle(pkt []byte, from *net.UDPAddr) {
	if flags, ok := ParseSetBroadcastFlags(pkt); ok {
		s.mu.Lock()
		if p, ok := s.peers[from.String()]; ok {
			p.flags = flags
		}
		s.mu.Unlock()
		return
	}
	if _, header, ok := PacketHeader(pkt); ok && header == HeaderGetBroadcastFlags {
		s.mu.Lock()
		var flags uint32
		if p, ok := s.peers[from.String()]; ok {
			flags = p.flags
		}
		s.mu.Unlock()
		_, _ = s.conn.WriteToUDP(BuildBroadcastFlagsReply(flags), from)
		return
	}
	if reply, ok := handshakeReply(pkt, s.serial); ok {
		_, _ = s.conn.WriteToUDP(reply, from)
		return
	}
	_, header, ok := PacketHeader(pkt)
	if !ok {
		return
	}
	if header == HeaderLogoff {
		s.dropPeer(from)
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
		s.markHeld(from, addr)
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
		s.markHeld(from, addr)
		s.echoLoco(from, addr)
		return
	}
	if addr, lo, hi, bits, ok := ParseSetLocoFunctionGroup(pkt); ok {
		var cur uint32
		if st, err := s.host.LocoState(addr); err == nil {
			cur = st.Functions
		}
		for fn := lo; fn <= hi; fn++ {
			want := bits&(1<<uint(fn-lo)) != 0
			have := cur&(1<<uint(fn)) != 0
			if want != have {
				_ = s.host.SetFunction(id, addr, fn, want)
			}
		}
		s.markHeld(from, addr)
		s.echoLoco(from, addr)
		return
	}
	if addr, ok := ParseGetLocoInfo(pkt); ok {
		s.mu.Lock()
		if p, ok := s.peers[from.String()]; ok {
			p.subs[addr] = struct{}{}
		}
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
