package commandstation

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/dcc-bigfred/proto/go/pkgs/z21"
)

var (
	_ Station              = (*Z21Roco)(nil)
	_ EmergencyStopper     = (*Z21Roco)(nil)
	_ TrackPowerController = (*Z21Roco)(nil)
)

// z21 broadcast flags (LAN_SET_BROADCASTFLAGS, §2.16).
const (
	// z21BcDrivingSwitching delivers LAN_X_LOCO_INFO for *subscribed*
	// locos (those queried via LAN_X_GET_LOCO_INFO) plus track power /
	// stop broadcasts.
	z21BcDrivingSwitching uint32 = z21.BcDrivingSwitching
	// z21BcAllLocos extends the flag above so the Z21 pushes
	// LAN_X_LOCO_INFO for *every* modified loco without per-address
	// subscription (FW ≥ 1.20). This is the flag intended for "PC
	// railroad automation software" — exactly dcc-bus's role — so it can
	// mirror changes made by external handsets it never subscribed to.
	z21BcAllLocos uint32 = z21.BcAllLocos
)

const (
	z21ReconnectBackoff  = 2 * time.Second
	z21HeartbeatInterval = 10 * time.Second
	z21HeartbeatTimeout  = 2 * time.Second
)

// NewZ21Roco dials a Roco/Fleischmann Z21 (or compatible) at netAddr:netPort
// over UDP and returns a Station. Typical port is 21105.
func NewZ21Roco(netAddr string, netPort uint16) (*Z21Roco, error) {
	roco := Z21Roco{
		addr:            fmt.Sprintf("%s:%d", netAddr, netPort),
		Timeout:         time.Second * 10,
		ReadInfoTimeout: 1500 * time.Millisecond,
		wasPowerCutOff:  false,
		syncCh:          make(chan []byte, 64),
		obsCh:           make(chan LocoObservation, 64),
		stop:            make(chan struct{}),
		metrics:         newZ21Metrics(),
	}
	roco.defaultSpeedSteps.Store(128)
	if err := roco.dial(); err != nil {
		return nil, err
	}
	go roco.readLoop()
	go roco.heartbeatLoop()
	return &roco, nil
}

// Z21Roco is a Station over Z21 LAN (UDP). It also implements
// TrackPowerController, StateObserver, and Z21MetricsSource.
type Z21Roco struct {
	addr   string
	conn   net.Conn
	connMu sync.RWMutex
	// reachable is false while a reconnect is in progress or the last
	// serial-number heartbeat failed.
	reachable    atomic.Bool
	reconnecting atomic.Bool

	// Timeout bounds CV programming-track read/verify cycles, which can
	// be slow on some decoders.
	Timeout time.Duration
	// ReadInfoTimeout bounds the much faster LAN_X_GET_LOCO_INFO reads
	// (GetSpeed / ListFunctions). It is kept short so a query never
	// stalls the shared UDP socket — and with it every other throttle
	// write — for seconds on an unresponsive loco.
	ReadInfoTimeout time.Duration
	wasPowerCutOff  bool
	// ioMu serializes request/response sequences. The Z21 is a single
	// UDP socket; only the read loop ever calls conn.Read, but ioMu
	// still serializes the request/await pairs so two callers cannot
	// interleave their sync windows.
	ioMu sync.Mutex

	// A single read-loop goroutine owns conn.Read and demultiplexes
	// every datagram: observations always flow to obsCh, while
	// request/response packets are forwarded to syncCh only while a
	// synchronous sequence is in flight (syncActive), mirroring the
	// LocoNet driver.
	syncCh     chan []byte
	syncActive atomic.Bool
	obsCh      chan LocoObservation
	stop       chan struct{}

	// broadcastsWanted is set when ObserveStates has requested
	// LAN_X_LOCO_INFO push; reconnect re-sends the flags because a
	// new UDP socket is a new Z21 LAN session.
	broadcastsWanted atomic.Bool
	// enableBroadcastsOnce lazily turns on LAN_X_LOCO_INFO push the
	// first time a consumer asks for observations.
	enableBroadcastsOnce sync.Once

	// fnStateCache keeps the last known function state bytes per locomotive.
	// Keyed by address; value is 5 bytes covering F0..F31 as in LAN_X_LOCO_INFO (DB4..DB8).
	fnStateCache map[LocoAddr]fnState
	fnStateMu    sync.Mutex

	// metrics holds lock-free hot-path counters. Always non-nil; bumping it is
	// near-free and OTel-agnostic (see z21_metrics.go).
	metrics *z21Metrics

	// lastSpeedSteps is the most recent SetSpeed steps (14/28/128), used by
	// EmergencyStop so 14/28-step layouts are not forced to 128.
	lastSpeedSteps atomic.Uint32
	// defaultSpeedSteps is the command-station catalogue (daemon SpeedSteps)
	// used by EmergencyStop before the first SetSpeed.
	defaultSpeedSteps atomic.Uint32
}

// infoTimeout returns the read deadline used for loco-info queries.
func (z *Z21Roco) infoTimeout() time.Duration {
	if z.ReadInfoTimeout > 0 {
		return z.ReadInfoTimeout
	}
	return z.Timeout
}

// fnState represents function bits F0..F31 for a single loco, as reported
// by LAN_X_LOCO_INFO. The layout follows DB4..DB8.
//
// Bit mapping (per Z21 spec, simplified):
//
//	DB4 (b7..b0): F0..F4 and direction bits (we only care about F0..F4 here)
//	DB5: F5..F12
//	DB6: F13..F20
//	DB7: F21..F28
//	DB8: F29..F31 (not all bits used)
type fnState struct {
	B0_4   byte // DB4
	B5_12  byte // DB5
	B13_20 byte // DB6
	B21_28 byte // DB7
	B29_31 byte // DB8
}

func (z *Z21Roco) currentConn() net.Conn {
	z.connMu.RLock()
	defer z.connMu.RUnlock()
	return z.conn
}

// Reachable reports whether the last UDP dial / serial heartbeat succeeded.
func (z *Z21Roco) Reachable() bool {
	return z.reachable.Load()
}

func (z *Z21Roco) dial() error {
	conn, err := net.Dial("udp", z.addr)
	if err != nil {
		return fmt.Errorf("UDP dial error while connecting to Roco Z21: %s", err)
	}
	z.connMu.Lock()
	old := z.conn
	z.conn = conn
	z.connMu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	z.fnStateMu.Lock()
	if z.fnStateCache == nil {
		z.fnStateCache = make(map[LocoAddr]fnState)
	}
	z.fnStateMu.Unlock()
	if z.syncCh == nil {
		z.syncCh = make(chan []byte, 64)
	}
	if z.obsCh == nil {
		z.obsCh = make(chan LocoObservation, 64)
	}
	if z.stop == nil {
		z.stop = make(chan struct{})
	}
	if z.metrics == nil {
		z.metrics = newZ21Metrics()
	}
	logrus.WithField("remote", z.addr).Info("z21 command station: UDP socket open")
	return nil
}

func (z *Z21Roco) doReconnect() {
	if !z.reconnecting.CompareAndSwap(false, true) {
		return
	}
	defer z.reconnecting.Store(false)
	z.reachable.Store(false)
	for {
		select {
		case <-z.stop:
			return
		default:
		}
		if err := z.dial(); err != nil {
			logrus.WithError(err).WithField("remote", z.addr).Warn("z21 reconnect failed, retrying")
			select {
			case <-z.stop:
				return
			case <-time.After(z21ReconnectBackoff):
			}
			continue
		}
		z.restoreBroadcasts()
		if err := z.pingSerial(); err != nil {
			logrus.WithError(err).WithField("remote", z.addr).Warn("z21 reconnect probe failed, retrying")
			select {
			case <-z.stop:
				return
			case <-time.After(z21ReconnectBackoff):
			}
			continue
		}
		return
	}
}

func (z *Z21Roco) heartbeatLoop() {
	// UDP Dial succeeds without a peer; probe immediately so /healthz
	// does not stay green for a full interval against a dead Railbox.
	if err := z.pingSerial(); err != nil {
		logrus.WithError(err).WithField("remote", z.addr).Warn("z21 initial heartbeat failed, reconnecting")
		z.doReconnect()
	}
	ticker := time.NewTicker(z21HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-z.stop:
			return
		case <-ticker.C:
			if err := z.pingSerial(); err != nil {
				logrus.WithError(err).WithField("remote", z.addr).Warn("z21 heartbeat failed, reconnecting")
				z.doReconnect()
			}
		}
	}
}

func (z *Z21Roco) pingSerial() error {
	z.ioMu.Lock()
	defer z.ioMu.Unlock()
	z.beginSync()
	defer z.endSync()
	if _, err := z.write(z21SerialNumberProbe); err != nil {
		return err
	}
	_, err := z.awaitMatching(z21HeartbeatTimeout, func(pkt []byte) bool {
		return len(pkt) >= 4 && binary.LittleEndian.Uint16(pkt[2:4]) == 0x0010
	})
	if err != nil {
		z.reachable.Store(false)
		return err
	}
	z.reachable.Store(true)
	return nil
}

// Z21MetricsSnapshot implements the Z21MetricsSource interface: it returns the
// current cumulative counters plus instantaneous gauges (function-state cache
// size, channel depths). OTel-free by design; the dcc-bus telemetry layer maps
// it onto instruments.
func (z *Z21Roco) Z21MetricsSnapshot() Z21MetricsSnapshot {
	s := z.metrics.snapshot()
	z.fnStateMu.Lock()
	s.FnCacheEntries = int64(len(z.fnStateCache))
	z.fnStateMu.Unlock()
	s.ObsQueueLen, s.ObsQueueCap = int64(len(z.obsCh)), int64(cap(z.obsCh))
	s.SyncQueueLen, s.SyncQueueCap = int64(len(z.syncCh)), int64(cap(z.syncCh))
	return s
}

func (z *Z21Roco) CleanUp() error {
	if z.wasPowerCutOff {
		logrus.Debug("Restoring power on programming track")
		z.buildTrackPowerOn()
	}
	select {
	case <-z.stop:
	default:
		close(z.stop)
	}
	logrus.Info("z21 command station: closing UDP socket")
	z.connMu.Lock()
	conn := z.conn
	z.conn = nil
	z.connMu.Unlock()
	z.reachable.Store(false)
	if conn != nil {
		return conn.Close()
	}
	return nil
}

func (Z *Z21Roco) markBuildTrackPowerOff() {
	logrus.Debug("Marking programmng track as to be powered off")
	Z.wasPowerCutOff = true
}

// ObserveStates implements StateObserver: on first call it enables the
// Z21 LAN_X_LOCO_INFO broadcast so the station pushes state changes —
// including those made by external handsets — to this client.
func (z *Z21Roco) ObserveStates() <-chan LocoObservation {
	z.broadcastsWanted.Store(true)
	z.enableBroadcastsOnce.Do(func() {
		if err := z.enableLocoInfoBroadcast(); err != nil {
			logrus.WithError(err).Warn("z21: enabling loco-info broadcast failed; push may not work")
		} else {
			logrus.Info("z21 command station: LAN_X_LOCO_INFO broadcast enabled (push)")
		}
	})
	return z.obsCh
}

func (z *Z21Roco) restoreBroadcasts() {
	if !z.broadcastsWanted.Load() {
		return
	}
	if err := z.enableLocoInfoBroadcast(); err != nil {
		logrus.WithError(err).Warn("z21: re-enabling loco-info broadcast after reconnect failed")
	}
}

// SubscribeLocoInfo implements LocoInfoSubscriber. It sends
// LAN_X_GET_LOCO_INFO (§4.1) which subscribes addr for unsolicited
// LAN_X_LOCO_INFO under broadcast flag 0x00000001 — the mechanism that
// works on every Z21 firmware, unlike the "all locos" flag 0x00010000
// (FW ≥ 1.24 only). It is fire-and-forget: the read loop turns the
// reply into a normal observation, so we do not open a sync window here.
func (z *Z21Roco) SubscribeLocoInfo(addr LocoAddr) error {
	z.ioMu.Lock()
	defer z.ioMu.Unlock()
	req := z.buildGetLocoInfo(addr)
	logrus.Debugf("req(LAN_X_GET_LOCO_INFO subscribe addr=%d): % X", addr, req)
	_, err := z.write(req)
	return err
}

// enableLocoInfoBroadcast sets LAN_SET_BROADCASTFLAGS so the Z21 pushes
// LAN_X_LOCO_INFO for every modified loco (§2.16 flags 0x1 | 0x10000).
func (z *Z21Roco) enableLocoInfoBroadcast() error {
	z.ioMu.Lock()
	defer z.ioMu.Unlock()
	req := buildSetBroadcastFlags(z21BcDrivingSwitching | z21BcAllLocos)
	logrus.Debugf("req(LAN_SET_BROADCASTFLAGS): % X", req)
	_, err := z.write(req)
	return err
}

// readLoop is the single owner of conn.Read. It splits each UDP
// datagram into the concatenated Z21 packets it may carry, feeds the
// observation pipeline, and forwards packets to the sync waiter while a
// request/response sequence is in flight.
func (z *Z21Roco) readLoop() {
	buf := make([]byte, 1500)
	for {
		select {
		case <-z.stop:
			return
		default:
		}

		conn := z.currentConn()
		if conn == nil {
			z.doReconnect()
			continue
		}
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, err := conn.Read(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			select {
			case <-z.stop:
				return
			default:
				z.metrics.incr(&z.metrics.rxErrors)
				logrus.WithError(err).Warn("z21 read error, reconnecting")
				z.doReconnect()
				continue
			}
		}
		for _, pkt := range z21.SplitDatagram(buf[:n]) {
			z.handlePacket(pkt)
		}
	}
}

func (z *Z21Roco) handlePacket(pkt []byte) {
	logrus.Debugf("z21 RX: % X", pkt)
	z.metrics.countRx(pkt, len(pkt))
	z.observe(pkt)
	if z.syncActive.Load() {
		// Copy: buf is reused by the read loop on the next datagram.
		cp := append([]byte(nil), pkt...)
		select {
		case z.syncCh <- cp:
		default:
			z.metrics.incr(&z.metrics.syncDropped)
		}
	}
}

// observe parses a LAN_X_LOCO_INFO packet, refreshes the function-state
// cache and emits a LocoObservation. Non-LOCO_INFO packets are ignored.
func (z *Z21Roco) observe(pkt []byte) {
	addr, state, speed, forward, ok := parseLocoInfoPacket(pkt)
	if !ok {
		return
	}
	z.fnStateMu.Lock()
	z.fnStateCache[addr] = state
	z.fnStateMu.Unlock()

	var fnMask, fnBits uint32
	for fn := 0; fn <= 31; fn++ {
		bit := uint32(1) << uint(fn)
		fnMask |= bit
		if z.extractFunctionBit(&state, fn) {
			fnBits |= bit
		}
	}
	z.emit(LocoObservation{
		Addr:         addr,
		HasSpeed:     true,
		Speed:        speed,
		HasForward:   true,
		Forward:      forward,
		FunctionMask: fnMask,
		FunctionBits: fnBits,
	})
}

func (z *Z21Roco) emit(obs LocoObservation) {
	select {
	case z.obsCh <- obs:
	default:
		z.metrics.incr(&z.metrics.obsDropped)
		logrus.Debug("z21: observation channel full, dropping update")
	}
}

// beginSync routes subsequent packets to the request/response waiter.
// Callers hold ioMu, so only one sequence runs at a time.
func (z *Z21Roco) beginSync() {
	z.drainSync()
	z.syncActive.Store(true)
}

func (z *Z21Roco) endSync() {
	z.syncActive.Store(false)
	z.drainSync()
}

func (z *Z21Roco) drainSync() {
	for {
		select {
		case <-z.syncCh:
		default:
			return
		}
	}
}

// awaitMatching waits up to timeout for a forwarded packet that matches.
func (z *Z21Roco) awaitMatching(timeout time.Duration, match func(pkt []byte) bool) ([]byte, error) {
	deadline := time.After(timeout)
	for {
		select {
		case <-z.stop:
			return nil, errors.New("command station closed")
		case pkt := <-z.syncCh:
			if match(pkt) {
				return pkt, nil
			}
		case <-deadline:
			z.metrics.incr(&z.metrics.syncTimeouts)
			return nil, errors.New("response timeout")
		}
	}
}

// splitZ21Datagram splits a UDP datagram into the individual Z21 packets
// it carries. Each packet is length-prefixed by its little-endian
// DataLen, and the Z21 may batch several into one datagram.
func splitZ21Datagram(b []byte) [][]byte {
	return z21.SplitDatagram(b)
}

// parseLocoInfoPacket decodes a complete LAN_X_LOCO_INFO packet (0xEF)
// into address, function bits and speed/direction. ok is false for any
// other packet.
func parseLocoInfoPacket(pkt []byte) (addr LocoAddr, state fnState, speed uint8, forward bool, ok bool) {
	info, ok := z21.ParseLocoInfo(pkt)
	if !ok {
		return 0, fnState{}, 0, false, false
	}
	addr = LocoAddr(info.Addr)
	speed, forward = info.Speed, info.Forward
	if len(pkt) > 9 {
		state.B0_4 = pkt[9]
	}
	if len(pkt) > 10 {
		state.B5_12 = pkt[10]
	}
	if len(pkt) > 11 {
		state.B13_20 = pkt[11]
	}
	if len(pkt) > 12 {
		state.B21_28 = pkt[12]
	}
	if len(pkt) > 13 {
		state.B29_31 = pkt[13]
	}
	return addr, state, speed, forward, true
}

// locoInfoForAddr reports whether pkt is a LAN_X_LOCO_INFO for addr.
func locoInfoForAddr(pkt []byte, addr LocoAddr) bool {
	a, _, _, _, ok := parseLocoInfoPacket(pkt)
	return ok && a == addr
}

func (z *Z21Roco) buildCVRequest(mode Mode, lcv LocoCV, isWriteRequest bool) ([]byte, error) {
	var err error
	var req []byte

	switch mode {
	case MainTrackMode:
		if isWriteRequest {
			req = z.buildPomWriteByte(lcv)
		} else {
			req = z.buildPomReadPacket(lcv)
		}
	case ProgrammingTrackMode:
		if isWriteRequest {
			req = z.buildProgWritePacket(lcv)
		} else {
			req = z.buildProgReadPacket(lcv.Cv)
		}
	default:
		return []byte{}, errors.New("unrecognized mode")
	}

	return req, err
}

func (z *Z21Roco) WriteCV(mode Mode, lcv LocoCV, options ...ctxOptions) error {
	z.ioMu.Lock()
	defer z.ioMu.Unlock()

	ctx := RequestContext{timeout: z.Timeout, verify: false, retries: 2, settle: 200}
	applyMethodsToCtx(&ctx, options)

	req, err := z.buildCVRequest(mode, lcv, true)
	if err != nil {
		return fmt.Errorf("cannot build CV request in WriteCV: %s", err.Error())
	}

	// we need to restore the power later on
	if mode == ProgrammingTrackMode {
		defer z.markBuildTrackPowerOff()
	}

	logrus.Debugf("Writing CV: loco=%d, CV%d=%d", lcv.LocoId, lcv.Cv.Num, lcv.Cv.Value)
	if _, writeErr := z.write(req); writeErr != nil {
		return fmt.Errorf("cannot write CV: %s", writeErr.Error())
	}

	if ctx.verify {
		logrus.Debug("Verifying written CV")
		time.Sleep(ctx.settle)
		res, readErr := z.readCVValue(mode, lcv, ctx.timeout, ctx.retries)
		if readErr != nil {
			return fmt.Errorf("cannot verify CV was written: %s", readErr.Error())
		}
		if res.value != byte(lcv.Cv.Value) {
			return fmt.Errorf("cannot write CV, the value differs after a write")
		}
	}

	return nil
}

// ReadCV reads a CV
func (z *Z21Roco) ReadCV(mode Mode, lcv LocoCV, options ...ctxOptions) (int, error) {
	z.ioMu.Lock()
	defer z.ioMu.Unlock()

	ctx := RequestContext{timeout: z.Timeout, verify: false, retries: 2, settle: 200}
	applyMethodsToCtx(&ctx, options)

	// we need to restore the power later on
	if mode == ProgrammingTrackMode {
		defer z.markBuildTrackPowerOff()
	}

	res, readErr := z.readCVValue(mode, lcv, ctx.timeout, ctx.retries)
	if readErr != nil {
		return 0, fmt.Errorf("cannot read CV: %s", readErr.Error())
	}
	return int(res.value), nil
}

// Sends a function request to the decoder
func (z *Z21Roco) SendFn(mode Mode, addr LocoAddr, num FuncNum, toggle bool) error {
	z.ioMu.Lock()
	defer z.ioMu.Unlock()

	if mode != MainTrackMode {
		return fmt.Errorf("SendFn: unsupported mode %s", mode)
	}

	fn := int(num)
	if fn < 0 || fn > 31 {
		return fmt.Errorf("SendFn: unsupported function number %d (must be 0-31)", num)
	}

	// Build and send the function command
	req := z.buildSetLocoFunction(addr, fn, toggle)
	logrus.Debugf("req(LAN_X_SET_LOCO_FUNCTION): %v", req)
	if _, err := z.write(req); err != nil {
		return fmt.Errorf("SendFn: cannot write function command: %s", err)
	}

	// Update our cache with the new state
	z.updateFunctionStateCache(addr, fn, toggle)

	return nil
}

// ListFunctions retrieves all active functions for a locomotive and returns their numbers
func (z *Z21Roco) ListFunctions(addr LocoAddr) ([]int, error) {
	z.ioMu.Lock()
	defer z.ioMu.Unlock()

	z.beginSync()
	defer z.endSync()

	// Query the command station using LAN_X_GET_LOCO_INFO
	req := z.buildGetLocoInfo(addr)
	logrus.Debugf("req(LAN_X_GET_LOCO_INFO): %v", req)
	if _, err := z.write(req); err != nil {
		return nil, fmt.Errorf("failed to send LAN_X_GET_LOCO_INFO: %w", err)
	}

	pkt, err := z.awaitMatching(z.infoTimeout(), func(p []byte) bool {
		return locoInfoForAddr(p, addr)
	})
	if err != nil {
		return nil, fmt.Errorf("failed to read LAN_X_LOCO_INFO response: %w", err)
	}
	logrus.Debugf("resp(LAN_X_LOCO_INFO): % X", pkt)

	_, state, _, _, _ := parseLocoInfoPacket(pkt)

	// Cache the state for future reference
	z.fnStateMu.Lock()
	z.fnStateCache[addr] = state
	z.fnStateMu.Unlock()

	// Extract all active functions (F0..F31)
	var activeFunctions []int
	for fnNum := 0; fnNum <= 31; fnNum++ {
		if z.extractFunctionBit(&state, fnNum) {
			activeFunctions = append(activeFunctions, fnNum)
		}
	}

	return activeFunctions, nil
}

type cvResult struct {
	cv     uint16 // 0=CV1 (N+1)
	value  byte
	source string // LAN_X_CV_RESULT/NACK/NACK_SC
}

func (res *cvResult) Error() error {
	switch res.source {
	// ok, we return a correct result
	case "LAN_X_CV_RESULT":
		return nil
	// below are errors returned by Command Station, so the network is okay, but the error is on the protocol side / input data
	case "LAN_X_CV_NACK":
		return fmt.Errorf("missing RailCom acknowledgement (NACK_SC)")
	case "LAN_X_CV_NACK_SC":
		return fmt.Errorf("short circuit (LAN_X_CV_NACK_SC)")
	}
	return fmt.Errorf("unknown error (%s)", res.source)
}

func (z *Z21Roco) parseCVResponse(pkt []byte) (cvResult, bool) {
	if len(pkt) < 6 {
		return cvResult{}, false
	}
	dataLen := binary.LittleEndian.Uint16(pkt[0:2])
	header := binary.LittleEndian.Uint16(pkt[2:4])
	if header != 0x0040 || int(dataLen) != len(pkt) {
		return cvResult{}, false
	}

	// RESULT: 64 14 CV_MSB CV_LSB Value XOR
	if len(pkt) >= 10 && pkt[4] == 0x64 && pkt[5] == 0x14 {
		return cvResult{
			cv:     (uint16(pkt[6]) << 8) | uint16(pkt[7]),
			value:  pkt[8],
			source: "LAN_X_CV_RESULT",
		}, true
	}
	// NACKs
	if pkt[4] == 0x61 && pkt[5] == 0x13 {
		return cvResult{source: "LAN_X_CV_NACK"}, true
	}
	if pkt[4] == 0x61 && pkt[5] == 0x12 {
		return cvResult{source: "LAN_X_CV_NACK_SC"}, true
	}
	return cvResult{}, false
}

// Sends and waits for LAN_X_CV_* (read or write-result). The reply is
// delivered by the read loop via syncCh; we filter for a CV packet.
func (z *Z21Roco) sendAndAwait(req []byte, timeout time.Duration) (cvResult, error) {
	z.beginSync()
	defer z.endSync()

	logrus.Debugf("z21.sendAndAwait: % X", req)
	if _, err := z.write(req); err != nil {
		return cvResult{}, err
	}
	var res cvResult
	pkt, err := z.awaitMatching(timeout, func(p []byte) bool {
		r, ok := z.parseCVResponse(p)
		if ok {
			res = r
		}
		return ok
	})
	if err != nil {
		return cvResult{}, err
	}
	_ = pkt
	switch res.source {
	case "LAN_X_CV_NACK":
		z.metrics.incr(&z.metrics.cvNacks)
	case "LAN_X_CV_NACK_SC":
		z.metrics.incr(&z.metrics.cvNackSC)
	}
	return res, nil
}

// readCVValue is reading the POM/PROG CV response
func (z *Z21Roco) readCVValue(mode Mode, lcv LocoCV, timeout time.Duration, retries uint8) (cvResult, error) {
	req, reqErr := z.buildCVRequest(mode, lcv, false)
	if reqErr != nil {
		return cvResult{}, fmt.Errorf("cannot build CV request: %s", reqErr)
	}

	var lastErr error
	for i := 0; i <= int(retries); i++ {
		logrus.Debugf("Try [%d/%d]", i, retries)
		res, err := z.sendAndAwait(req, timeout)
		if err == nil {
			if responseErr := res.Error(); responseErr != nil {
				lastErr = fmt.Errorf("cannot read CV: %s", responseErr.Error())
				err = lastErr
				continue
			}

			return res, nil
		}
		lastErr = err
		time.Sleep(200 * time.Millisecond)
	}
	return cvResult{}, lastErr
}

// extractFunctionBit extracts the state of a specific function from fnState
func (z *Z21Roco) extractFunctionBit(state *fnState, fnNum int) bool {
	switch {
	case fnNum == 0:
		// F0 is bit 4 in DB4
		return (state.B0_4 & 0x10) != 0
	case fnNum >= 1 && fnNum <= 4:
		// F1-F4 are bits 0-3 in DB4
		return (state.B0_4 & (1 << (fnNum - 1))) != 0
	case fnNum >= 5 && fnNum <= 12:
		// F5-F12 are bits 0-7 in DB5
		return (state.B5_12 & (1 << (fnNum - 5))) != 0
	case fnNum >= 13 && fnNum <= 20:
		// F13-F20 are bits 0-7 in DB6
		return (state.B13_20 & (1 << (fnNum - 13))) != 0
	case fnNum >= 21 && fnNum <= 28:
		// F21-F28 are bits 0-7 in DB7
		return (state.B21_28 & (1 << (fnNum - 21))) != 0
	case fnNum >= 29 && fnNum <= 31:
		// F29-F31 are bits 0-2 in DB8
		return (state.B29_31 & (1 << (fnNum - 29))) != 0
	default:
		return false
	}
}

// updateFunctionStateCache updates the cached function state for a locomotive
func (z *Z21Roco) updateFunctionStateCache(addr LocoAddr, fnNum int, on bool) {
	z.fnStateMu.Lock()
	defer z.fnStateMu.Unlock()

	state, ok := z.fnStateCache[addr]
	if !ok {
		// Initialize empty state if not present
		state = fnState{}
	}

	// Update the appropriate bit
	switch {
	case fnNum == 0:
		if on {
			state.B0_4 |= 0x10
		} else {
			state.B0_4 &^= 0x10
		}
	case fnNum >= 1 && fnNum <= 4:
		mask := byte(1 << (fnNum - 1))
		if on {
			state.B0_4 |= mask
		} else {
			state.B0_4 &^= mask
		}
	case fnNum >= 5 && fnNum <= 12:
		mask := byte(1 << (fnNum - 5))
		if on {
			state.B5_12 |= mask
		} else {
			state.B5_12 &^= mask
		}
	case fnNum >= 13 && fnNum <= 20:
		mask := byte(1 << (fnNum - 13))
		if on {
			state.B13_20 |= mask
		} else {
			state.B13_20 &^= mask
		}
	case fnNum >= 21 && fnNum <= 28:
		mask := byte(1 << (fnNum - 21))
		if on {
			state.B21_28 |= mask
		} else {
			state.B21_28 &^= mask
		}
	case fnNum >= 29 && fnNum <= 31:
		mask := byte(1 << (fnNum - 29))
		if on {
			state.B29_31 |= mask
		} else {
			state.B29_31 &^= mask
		}
	}

	z.fnStateCache[addr] = state
}

// SetSpeed sets the speed and direction of a locomotive
// speed: 0=stop, 1=emergency stop, 2+ for actual speed (max depends on speedSteps)
// forward: true for forward, false for reverse
// speedSteps: 14, 28, or 128
func (z *Z21Roco) SetSpeed(addr LocoAddr, speed uint8, forward bool, speedSteps uint8) error {
	z.ioMu.Lock()
	defer z.ioMu.Unlock()

	// Convert speedSteps to the DB0 "S" nibble. NOTE the asymmetry vs.
	// LAN_X_LOCO_INFO: the SET command (§4.2) uses S=3 for DCC 128, while
	// the INFO reply (§4.4) reports KKK=4 for the same mode. Sending S=4
	// here (the INFO value) is undefined and the Z21 mis-drives the loco.
	var speedStepsProto uint8
	switch speedSteps {
	case 14:
		speedStepsProto = 0
	case 28:
		speedStepsProto = 2
	case 128:
		speedStepsProto = 3
	default:
		return fmt.Errorf("invalid speed steps: %d (must be 14, 28, or 128)", speedSteps)
	}
	z.lastSpeedSteps.Store(uint32(speedSteps))

	// Build and send the speed command
	req := z.buildSetLocoSpeed(addr, speed, forward, speedStepsProto)
	logrus.Debugf("req(LAN_X_SET_LOCO_DRIVE): % X", req)
	if _, err := z.write(req); err != nil {
		return fmt.Errorf("SetSpeed: cannot write speed command: %w", err)
	}

	return nil
}

// GetSpeed retrieves the current speed and direction of a locomotive
// Returns: speed (0-127), forward (true for forward, false for reverse), error
func (z *Z21Roco) GetSpeed(addr LocoAddr) (uint8, bool, error) {
	z.ioMu.Lock()
	defer z.ioMu.Unlock()
	z.beginSync()
	defer z.endSync()

	// Query the command station using LAN_X_GET_LOCO_INFO
	req := z.buildGetLocoInfo(addr)
	logrus.Debugf("req(LAN_X_GET_LOCO_INFO): %v", req)
	if _, err := z.write(req); err != nil {
		return 0, false, fmt.Errorf("failed to send LAN_X_GET_LOCO_INFO: %w", err)
	}

	pkt, err := z.awaitMatching(z.infoTimeout(), func(p []byte) bool {
		return locoInfoForAddr(p, addr)
	})
	if err != nil {
		return 0, false, fmt.Errorf("failed to read LAN_X_LOCO_INFO response: %w", err)
	}
	logrus.Debugf("resp(LAN_X_LOCO_INFO): % X", pkt)

	_, _, speed, forward, _ := parseLocoInfoPacket(pkt)
	return speed, forward, nil
}

// EmergencyStop sends LAN_X_SET_LOCO_DRIVE with V=1 (per-loco e-stop).
// Direction (R) is preserved. This is not LAN_X_SET_STOP (layout-wide halt).
func (z *Z21Roco) EmergencyStop(addr LocoAddr, forward bool) error {
	steps := uint8(z.lastSpeedSteps.Load())
	if steps != 14 && steps != 28 && steps != 128 {
		steps = uint8(z.defaultSpeedSteps.Load())
	}
	if steps != 14 && steps != 28 && steps != 128 {
		steps = 128
	}
	return z.SetSpeed(addr, 1, forward, steps)
}

// SetSpeedSteps records the command-station catalogue used by EmergencyStop
// when no SetSpeed has run yet (BigFred daemon SpeedSteps).
func (z *Z21Roco) SetSpeedSteps(steps uint8) {
	if steps != 14 && steps != 28 && steps != 128 {
		steps = 128
	}
	z.defaultSpeedSteps.Store(uint32(steps))
}

// encodeLocoDriveDB3 builds DB3 (RVVVVVVV) for LAN_X_SET_LOCO_DRIVE (§4.2).
// UI/API speed semantics: 0=stop, 1=e-stop, 2+ = drive steps (max depends
// on speedSteps).
//
// The R bit (bit 7) is the *direction* and MUST always reflect the
// desired direction — including at Stop and E-Stop. Zeroing it at stop
// (e.g. 0x00 for "stop") commands reverse, which flips the loco's
// direction every time it stops. Stop is therefore R + V=0, E-Stop is
// R + V=1.
func encodeLocoDriveDB3(speed uint8, forward bool, speedSteps uint8) byte {
	return z21.EncodeDriveDB3(speed, forward, speedSteps)
}

// decodeLocoDriveFromLocoInfo decodes DB2/DB3 from LAN_X_LOCO_INFO (§4.4).
// DB3 is RVVVVVVV: bit 7 = direction (1=forward), low bits = speed.
// DB2 low 3 bits (KKK) select 14 / 28 / 128 speed-step encoding.
func decodeLocoDriveFromLocoInfo(db2, db3 byte) (speed uint8, forward bool) {
	return z21.DecodeDriveFromLocoInfo(db2, db3)
}
