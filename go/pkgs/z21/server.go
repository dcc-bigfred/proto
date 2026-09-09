package z21

import (
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/dcc-bigfred/proto/go/pkgs/drive"
)

const (
	peerTTL       = 60 * time.Second
	peerSweep     = 10 * time.Second
	readDeadline  = 1 * time.Second
	loopBackoff   = 50 * time.Millisecond
	maxLoopErrors = 32
)

type z21config struct {
	keyFn    func(*net.UDPAddr) drive.ClientID
	serial   uint32
	ttl      time.Duration
	sysState []byte
	onError  func(error)
}

func defaultZ21Config() z21config {
	return z21config{
		keyFn:  func(a *net.UDPAddr) drive.ClientID { return drive.ClientID(a.String()) },
		serial: 258_000_001,
		ttl:    peerTTL,
	}
}

// Option configures Listen.
type Option func(*z21config)

// WithClientKeyFunc keys peers (IP stickiness: IP-only vs ip:port).
func WithClientKeyFunc(f func(*net.UDPAddr) drive.ClientID) Option {
	return func(c *z21config) {
		if f != nil {
			c.keyFn = f
		}
	}
}

// WithSerial sets LAN_GET_SERIAL_NUMBER.
func WithSerial(n uint32) Option {
	return func(c *z21config) {
		if n != 0 {
			c.serial = n
		}
	}
}

// WithPeerTTL sets idle eviction. Zero disables the sweeper.
func WithPeerTTL(d time.Duration) Option {
	return func(c *z21config) { c.ttl = d }
}

// WithSystemStatePayload replaces the 16-byte LAN_SYSTEMSTATE_DATACHANGED
// body (BigFred DefaultSystemState / cfg.SystemState).
func WithSystemStatePayload(data []byte) Option {
	return func(c *z21config) {
		if len(data) == 16 {
			c.sysState = append([]byte(nil), data...)
		}
	}
}

// WithErrorHandler is invoked on read-loop errors that are not shutdown.
func WithErrorHandler(h func(error)) Option {
	return func(c *z21config) { c.onError = h }
}

// Server is an inbound Z21 LAN UDP listener.
type Server struct {
	host      drive.DriveHost
	cfg       z21config
	conn      *net.UDPConn
	done      chan struct{}
	dispatch  *dispatcher
	loops     sync.WaitGroup
	closeOnce sync.Once

	mu    sync.Mutex
	peers map[drive.ClientID]*peer
}

type peer struct {
	id       drive.ClientID
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
func Listen(bind string, host drive.DriveHost, opts ...Option) (*Server, error) {
	if bind == "" {
		bind = ":21105"
	}
	if host == nil {
		host = nopHost{}
	}
	cfg := defaultZ21Config()
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
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
		host:     host,
		cfg:      cfg,
		conn:     conn,
		done:     make(chan struct{}),
		peers:    make(map[drive.ClientID]*peer),
		dispatch: newDispatcher(dispatchShards, dispatchShardBuf),
	}
	s.loops.Add(1)
	go func() {
		defer s.loops.Done()
		s.readLoop()
	}()
	if cfg.ttl > 0 {
		s.loops.Add(1)
		go func() {
			defer s.loops.Done()
			s.ttlLoop()
		}()
	}
	return s, nil
}

// Addr is the bound UDP address.
func (s *Server) Addr() net.Addr { return s.conn.LocalAddr() }

// Close stops the listener.
func (s *Server) Close() error {
	var err error
	s.closeOnce.Do(func() {
		close(s.done)
		err = s.conn.Close()
		s.loops.Wait()
		s.dispatch.close()
	})
	return err
}

func (s *Server) clientID(from *net.UDPAddr) drive.ClientID {
	return s.cfg.keyFn(from)
}

// SendLocoInfo writes LAN_X_LOCO_INFO to one peer.
func (s *Server) SendLocoInfo(client drive.ClientID, st drive.LocoState) {
	s.mu.Lock()
	p := s.peers[client]
	var addr *net.UDPAddr
	if p != nil {
		addr = cloneUDPAddr(p.addr)
	}
	s.mu.Unlock()
	if addr == nil {
		return
	}
	_, _ = s.conn.WriteToUDP(BuildLocoInfo(st), addr)
}

// SendTo writes a raw LAN frame to one peer.
func (s *Server) SendTo(client drive.ClientID, frame []byte) {
	s.mu.Lock()
	p := s.peers[client]
	var addr *net.UDPAddr
	if p != nil {
		addr = cloneUDPAddr(p.addr)
	}
	s.mu.Unlock()
	if addr == nil {
		return
	}
	_, _ = s.conn.WriteToUDP(frame, addr)
}

// Disconnect drops a peer and Releases held locos.
func (s *Server) Disconnect(client drive.ClientID) {
	s.dropPeerID(client, false)
}

// BroadcastFlags returns the stored flags for client.
func (s *Server) BroadcastFlags(client drive.ClientID) uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.peers[client]; p != nil {
		return p.flags
	}
	return 0
}

// NotifyLocoState pushes LAN_X_LOCO_INFO to peers whose broadcast flags match.
func (s *Server) NotifyLocoState(st drive.LocoState) {
	s.notifyLocoState(st, "")
}

// NotifyLocoStateExcept skips origin.
func (s *Server) NotifyLocoStateExcept(st drive.LocoState, origin drive.ClientID) {
	s.notifyLocoState(st, origin)
}

func (s *Server) notifyLocoState(st drive.LocoState, origin drive.ClientID) {
	pkt := BuildLocoInfo(st)
	s.mu.Lock()
	targets := make([]*net.UDPAddr, 0, len(s.peers))
	for id, p := range s.peers {
		if origin != "" && id == origin {
			continue
		}
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
	consecutive := 0
	for {
		select {
		case <-s.done:
			return
		default:
		}
		_ = s.conn.SetReadDeadline(time.Now().Add(readDeadline))
		n, from, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			if isClosedNetErr(err) {
				return
			}
			select {
			case <-s.done:
				return
			default:
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				consecutive = 0
				continue
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
		payload := append([]byte(nil), buf[:n]...)
		fromCopy := cloneUDPAddr(from)
		id := s.clientID(fromCopy)
		s.dispatch.dispatch(string(id), func() {
			s.serveDatagram(payload, fromCopy)
		})
	}
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

func (s *Server) serveDatagram(payload []byte, from *net.UDPAddr) {
	s.notePeer(from)
	if h, ok := s.host.(SessionHooks); ok {
		h.OnActivity(s.clientID(from))
	}
	for _, pkt := range SplitDatagram(payload) {
		s.handle(pkt, from)
	}
}

func cloneUDPAddr(a *net.UDPAddr) *net.UDPAddr {
	if a == nil {
		return nil
	}
	out := *a
	if a.IP != nil {
		out.IP = append(net.IP(nil), a.IP...)
	}
	return &out
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
	cutoff := now.Add(-s.cfg.ttl)
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
		for addr := range p.held {
			release = append(release, held{key, addr})
		}
		delete(s.peers, key)
	}
	s.mu.Unlock()
	for _, h := range release {
		s.host.Release(h.id, h.addr)
	}
}

func (s *Server) notePeer(from *net.UDPAddr) *peer {
	id := s.clientID(from)
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.peers[id]
	if !ok {
		p = &peer{
			id:   id,
			addr: from,
			subs: make(map[uint16]struct{}),
			held: make(map[uint16]struct{}),
		}
		s.peers[id] = p
	}
	p.addr = from
	p.lastSeen = time.Now()
	return p
}

func (s *Server) markHeld(id drive.ClientID, addr uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.peers[id]; ok {
		p.held[addr] = struct{}{}
	}
}

func (s *Server) dropPeer(from *net.UDPAddr) {
	s.dropPeerID(s.clientID(from), true)
}

func (s *Server) dropPeerID(id drive.ClientID, logoff bool) {
	s.mu.Lock()
	p, ok := s.peers[id]
	if !ok {
		s.mu.Unlock()
		return
	}
	held := make([]uint16, 0, len(p.held))
	for addr := range p.held {
		held = append(held, addr)
	}
	delete(s.peers, id)
	s.mu.Unlock()
	if logoff {
		if h, ok := s.host.(SessionHooks); ok {
			h.OnLogoff(id)
		}
	}
	for _, addr := range held {
		s.host.Release(id, addr)
	}
}

func (s *Server) systemStateFrame() []byte {
	if len(s.cfg.sysState) == 16 {
		return BuildLAN(HeaderSystemStateData, s.cfg.sysState)
	}
	return buildSystemStateReply()
}

func (s *Server) gated(id drive.ClientID, op DriveOp, addr uint16, pkt []byte) bool {
	g, ok := s.host.(DriveGate)
	if !ok {
		return false
	}
	return g.Drive(id, op, addr, pkt)
}

func (s *Server) handle(pkt []byte, from *net.UDPAddr) {
	id := s.clientID(from)
	if flags, ok := ParseSetBroadcastFlags(pkt); ok {
		s.mu.Lock()
		if p, ok := s.peers[id]; ok {
			p.flags = flags
		}
		s.mu.Unlock()
		if h, ok := s.host.(SessionHooks); ok {
			h.OnBroadcastFlags(id, flags)
		}
		if flags&BcSystemState != 0 {
			_, _ = s.conn.WriteToUDP(s.systemStateFrame(), from)
		}
		return
	}
	if _, header, ok := PacketHeader(pkt); ok && header == HeaderGetBroadcastFlags {
		// Match BigFred v1 handshake: GET always returns zeros. SET still
		// stores flags for NotifyLocoState / wantsLoco.
		_, _ = s.conn.WriteToUDP(BuildBroadcastFlagsReply(0), from)
		return
	}
	if _, header, ok := PacketHeader(pkt); ok && header == HeaderSystemStateGetData {
		_, _ = s.conn.WriteToUDP(s.systemStateFrame(), from)
		return
	}
	if reply, ok := handshakeReply(pkt, s.cfg.serial); ok {
		_, _ = s.conn.WriteToUDP(reply, from)
		return
	}
	_, header, ok := PacketHeader(pkt)
	if !ok {
		return
	}
	switch header {
	case HeaderLogoff:
		s.dropPeer(from)
		return
	case HeaderRMBusGetData:
		group := byte(0)
		if len(pkt) >= 5 {
			group = pkt[4]
		}
		_, _ = s.conn.WriteToUDP(BuildRMBusDataChanged(group), from)
		return
	case HeaderGetLocoMode:
		if len(pkt) >= 6 {
			addr := binary.BigEndian.Uint16(pkt[4:6])
			_, _ = s.conn.WriteToUDP(BuildLocoModeReply(addr), from)
		}
		return
	case HeaderLocoNetFromLAN:
		return
	case HeaderLanKeepalive, HeaderLanSessionProbe:
		_, _ = s.conn.WriteToUDP(buildStatusChangedReply(), from)
		return
	}
	if header != HeaderXBus {
		return
	}
	if ParseSetStop(pkt) {
		if s.gated(id, DriveSetStop, 0, pkt) {
			return
		}
		s.mu.Lock()
		var held []uint16
		if p := s.peers[id]; p != nil {
			for addr := range p.held {
				held = append(held, addr)
			}
		}
		s.mu.Unlock()
		for _, addr := range held {
			_ = s.host.SetSpeed(id, addr, 1, true, 128)
		}
		_, _ = s.conn.WriteToUDP(BuildBCStopped(), from)
		return
	}
	if addr, ok := ParseSetLocoEStop(pkt); ok {
		if s.gated(id, DriveLocoEStop, addr, pkt) {
			return
		}
		_ = s.host.SetSpeed(id, addr, 1, true, 128)
		s.markHeld(id, addr)
		s.echoLoco(from, addr)
		return
	}
	if addr, ok := ParsePurgeLoco(pkt); ok {
		if s.gated(id, DrivePurge, addr, pkt) {
			return
		}
		s.host.Release(id, addr)
		s.mu.Lock()
		if p := s.peers[id]; p != nil {
			delete(p.held, addr)
			delete(p.subs, addr)
		}
		s.mu.Unlock()
		return
	}
	if addr, cvWire, value, ok := ParsePomWriteByte(pkt); ok {
		s.handleCV(id, from, CVPomWrite, addr, cvWire, value)
		return
	}
	if addr, cvWire, ok := ParsePomReadByte(pkt); ok {
		s.handleCV(id, from, CVPomRead, addr, cvWire, 0)
		return
	}
	if cvWire, value, ok := ParseProgWrite(pkt); ok {
		s.handleCV(id, from, CVProgWrite, 0, cvWire, value)
		return
	}
	if cvWire, ok := ParseProgRead(pkt); ok {
		s.handleCV(id, from, CVProgRead, 0, cvWire, 0)
		return
	}
	if addr, speed, forward, ok := ParseSetLocoDrive(pkt); ok {
		if s.gated(id, DriveSetSpeed, addr, pkt) {
			return
		}
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
		s.markHeld(id, addr)
		s.echoLoco(from, addr)
		return
	}
	if addr, fn, on, toggle, ok := ParseSetLocoFunction(pkt); ok {
		if s.gated(id, DriveSetFunction, addr, pkt) {
			return
		}
		if toggle {
			st, err := s.host.LocoState(addr)
			if err == nil {
				on = st.Functions&(1<<uint(fn)) == 0
			}
		}
		_ = s.host.SetFunction(id, addr, uint8(fn), on)
		s.markHeld(id, addr)
		s.echoLoco(from, addr)
		return
	}
	if addr, lo, hi, bits, ok := ParseSetLocoFunctionGroup(pkt); ok {
		if s.gated(id, DriveSetFunctionGroup, addr, pkt) {
			return
		}
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
		s.markHeld(id, addr)
		s.echoLoco(from, addr)
		return
	}
	if addr, ok := ParseGetLocoInfo(pkt); ok {
		if s.gated(id, DriveGetLocoInfo, addr, pkt) {
			return
		}
		s.mu.Lock()
		if p, ok := s.peers[id]; ok {
			p.subs[addr] = struct{}{}
		}
		s.mu.Unlock()
		s.echoLoco(from, addr)
		return
	}
	if on, ok := ParseTrackPower(pkt); ok {
		if s.gated(id, DriveTrackPower, 0, pkt) {
			return
		}
		_ = s.host.SetTrackPower(id, on)
		s.NotifyTrackPower(on)
	}
}

func (s *Server) handleCV(id drive.ClientID, from *net.UDPAddr, op CVOp, addr uint16, cvWire uint16, value uint8) {
	if g, ok := s.host.(CVGate); ok {
		handled, reply := g.CV(id, op, addr, cvWire, value)
		if handled {
			if len(reply) > 0 {
				_, _ = s.conn.WriteToUDP(reply, from)
			}
			return
		}
	}
	_, _ = s.conn.WriteToUDP(BuildCvNack(), from)
}

func (s *Server) echoLoco(from *net.UDPAddr, addr uint16) {
	st, err := s.host.LocoState(addr)
	if err != nil {
		return
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
