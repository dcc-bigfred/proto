package commandstation

import (
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"
)

type lnTransport interface {
	WritePacket(pkt []byte) error
	Close() error
}

// lnRxCounter is implemented by transports that can report how many bytes they
// have read off the wire (serial). Used to tell "dead bus" apart from "module
// did not answer" in LNCV diagnostics.
type lnRxCounter interface {
	RxByteCount() uint64
}

// rxByteCount returns the number of bytes read off the wire, or 0 if the
// transport does not track it.
func (l *LocoNet) rxByteCount() uint64 {
	if c, ok := l.t.(lnRxCounter); ok {
		return c.RxByteCount()
	}
	return 0
}

type LocoNet struct {
	t lnTransport

	// timeout bounds request/response sequences that legitimately take a while
	// (LNCV programming, manual dispatch). Slot speed/function ops use the much
	// shorter slotTimeout instead.
	timeout time.Duration

	// slotTimeout bounds long request/response sequences (service-mode CV reads).
	slotTimeout time.Duration

	// slotReplyTimeout bounds slot acquire round-trips (LOCO_ADR, RQ_SL_DATA,
	// NULL MOVE). LocoNet replies arrive well under 30 ms; a tight bound keeps
	// a lost reply from stalling slot acquisition — and thus the whole fleet —
	// for hundreds of milliseconds.
	slotReplyTimeout time.Duration

	// TX pacing: a dedicated writer goroutine (txLoop) enforces the inter-frame
	// gap and serializes transport writes so a slow adapter cannot block callers.
	lastTxAt time.Time
	minTxGap time.Duration

	// txCh carries normal-priority frames; txLowCh carries keepalive refreshes;
	// txEstopCh carries emergency stops ahead of both.
	txCh      chan lnTxJob
	txLowCh   chan lnTxJob
	txEstopCh chan lnTxJob

	// keepaliveInterval is how often active slots are re-touched so the master
	// does not purge them to COMMON after ~200 s of inactivity (spec §4.3).
	keepaliveInterval time.Duration

	// rxCh receives every packet the transport reads off the bus.
	// A single dispatch goroutine owns it (see dispatch): it updates
	// the observation pipeline for ALL traffic — including packets
	// authored by external throttles — and forwards packets to syncCh
	// only while a request/response sequence is in flight.
	rxCh chan lnPacket
	// syncCh carries packets to the request/response waiters
	// (ensureSlotLocked / querySlotLocked). The dispatcher feeds it
	// only when syncActive is set so unsolicited bus traffic does not
	// pile up while nobody is waiting.
	syncCh     chan lnPacket
	syncActive atomic.Bool

	// obsCh streams observed state changes to StateObserver consumers.
	obsCh chan LocoObservation
	// obsCoalesce batches observations per address before obsCh.
	obsCoalesce *obsCoalescer

	stop chan struct{}

	// serialize request/response sequences
	reqMu sync.Mutex

	// state caches
	slotMu   sync.Mutex
	slotByAd map[LocoAddr]byte
	slotAddr map[byte]LocoAddr // reverse map, needed to attribute bus traffic
	// slotAcquiredAt records when each slot was last validated via a fresh
	// command-station round trip, so AcquireSlot can skip redundant
	// re-validation during reconnect storms (the keepalive loop keeps the
	// slot IN_USE in the meantime). Guarded by slotMu.
	slotAcquiredAt map[LocoAddr]time.Time

	// fnTxMu guards fnTxLocks, which hands out a per-address mutex that
	// serializes the read-modify-write of the shared function bytes. F0..F4
	// (DIRF) and F5..F8 (SND) are each a single byte / DCC function group, so
	// a concurrent toggle (e.g. the dead-man horn pulse) or a direction
	// change for the same loco must not interleave and transmit a stale bit.
	fnTxMu    sync.Mutex
	fnTxLocks map[LocoAddr]*sync.Mutex

	stateMu     sync.Mutex
	dirfByA     map[LocoAddr]byte
	sndByA      map[LocoAddr]byte
	spdByA      map[LocoAddr]byte   // last commanded/observed slot speed, for keepalive
	speedGenByA map[LocoAddr]uint64 // per-address SetSpeed generation, for TX coalescing
	// stat1ByA caches the last STAT1 from a slot read for owned locos so
	// ReleaseSlot can write COMMON without wiping decoder-type bits (D2–D0).
	stat1ByA map[LocoAddr]byte
	// extFnByA caches functions F9..F28 per address. These are NOT held
	// in the command-station slot (they ride immediate DCC packets), so
	// the only authoritative copy is the one we keep here, updated both
	// when WE send and when we observe such a packet on the shared bus.
	extFnByA map[LocoAddr]uint32

	// metrics holds lock-free hot-path counters. Always non-nil; bumping it is
	// near-free and OTel-agnostic (see loconet_metrics.go).
	metrics *lnMetrics

	// csSlotStaLast holds the last observed SL_STA per locomotive slot (0..119)
	// for CS-wide occupy/release counters. csSlotStaUnknown means never seen.
	csSlotStaLast [120]atomic.Uint32

	// slotBreakerUntil is a unix-nano deadline; while in the future, validateSlot
	// fails fast after repeated acquire timeouts (circuit breaker).
	slotBreakerUntil atomic.Int64

	// allocatePhysical, when true (default), enforces PE 1.0 exclusive slot
	// ownership: refuse drive acquire when the slot is already IN_USE by
	// another throttle. When false, BigFred may piggyback on IN_USE slots.
	allocatePhysical atomic.Bool

	// slotObs is the optional SlotObserver notified when one of OUR owned
	// slots becomes IN_USE or is released (COMMON/purge/reassign). External
	// throttles' slots are not reported here because the driver does not map
	// their slot number to a LocoAddr. nil-safe; calls are non-blocking.
	slotObs atomic.Pointer[SlotObserver]

	// keepaliveIdx rotates round-robin across cached slots for spread refreshes.
	keepaliveIdx int
}

// LocoNet TX/timeout tuning. These trade a touch of latency for the ability to
// drive many locomotives at once over the 16.66 kbit/s bus.
const (
	// lnDefaultMinTxGap paces transmissions to ~the bus drain rate. A 4-byte
	// LocoNet frame plus its mandatory CD backoff occupies ~3.6 ms of bus time,
	// so a ~5 ms floor keeps the driver from outrunning the wire and overflowing
	// the transport buffer when many locomotives move at once.
	lnDefaultMinTxGap = 5 * time.Millisecond

	// lnDefaultSlotTimeout bounds long request/response sequences (programming).
	lnDefaultSlotTimeout = 600 * time.Millisecond

	// lnDefaultSlotReplyTimeout bounds slot acquire round-trips on a healthy bus.
	lnDefaultSlotReplyTimeout = 200 * time.Millisecond

	// lnSlotAcquireRetries retries a timed-out slot acquisition once before
	// giving up, since a single dropped reply is common on a busy bus.
	lnSlotAcquireRetries = 1

	// lnKeepaliveInterval re-touches active slots well within the ~200 s purge
	// window (spec §4.3 recommends ~100 s).
	lnKeepaliveInterval = 90 * time.Second

	// lnMaxOwnedSlots is a soft cap on how many locomotive slots BigFred may
	// hold IN_USE at once, leaving headroom on the master's 120-slot table for
	// physical throttles.
	lnMaxOwnedSlots = 100

	// lnSlotBreakerCooldown pauses slot acquisitions after repeated failures so
	// a dead bus does not queue the whole fleet behind reqMu timeouts.
	lnSlotBreakerCooldown = 5 * time.Second

	// lnKeepaliveTickInterval spreads slot refreshes round-robin instead of
	// bursting every cached locomotive at once.
	lnKeepaliveTickInterval = 2 * time.Second

	// csSlotStaUnknown is the initial csSlotStaLast sentinel (not a valid SL_STA).
	csSlotStaUnknown = 0xFF
)

func newLocoNetBase() *LocoNet {
	ln := &LocoNet{
		timeout:           4 * time.Second,
		slotTimeout:       lnDefaultSlotTimeout,
		slotReplyTimeout:  lnDefaultSlotReplyTimeout,
		minTxGap:          lnDefaultMinTxGap,
		keepaliveInterval: lnKeepaliveInterval,
		txCh:              make(chan lnTxJob, 64),
		txLowCh:           make(chan lnTxJob, 32),
		txEstopCh:         make(chan lnTxJob, 8),
		rxCh:              make(chan lnPacket, 64),
		syncCh:            make(chan lnPacket, 64),
		obsCh:             make(chan LocoObservation, 64),
		stop:              make(chan struct{}),
		slotByAd:          make(map[LocoAddr]byte),
		slotAddr:          make(map[byte]LocoAddr),
		slotAcquiredAt:    make(map[LocoAddr]time.Time),
		fnTxLocks:         make(map[LocoAddr]*sync.Mutex),
		dirfByA:           make(map[LocoAddr]byte),
		sndByA:            make(map[LocoAddr]byte),
		spdByA:            make(map[LocoAddr]byte),
		speedGenByA:       make(map[LocoAddr]uint64),
		stat1ByA:          make(map[LocoAddr]byte),
		extFnByA:          make(map[LocoAddr]uint32),
		metrics:           newLnMetrics(),
	}
	ln.allocatePhysical.Store(true)
	ln.obsCoalesce = newObsCoalescer(ln.obsCh, lnObsCoalesceTick, ln.stop, &ln.metrics.obsDropped)
	return ln
}

// MetricsSnapshot implements the MetricsSource interface: it returns the
// current cumulative counters plus instantaneous gauges (active slots, channel
// depths) and any reliability counters the transport tracks. OTel-free by
// design; the dcc-bus telemetry layer maps it onto instruments.
func (l *LocoNet) MetricsSnapshot() LnMetricsSnapshot {
	s := l.metrics.snapshot()

	// RX bytes come from the transport (it counts raw bytes off the wire).
	s.RxBytes = l.rxByteCount()

	// Fold in transport-level reliability counters when available.
	if st, ok := l.t.(lnStatsTransport); ok {
		ts := st.lnTransportStats()
		if ts.RxBytes > 0 {
			s.RxBytes = ts.RxBytes
		}
		s.BadChecksum = ts.BadChecksum
		s.Reconnects = ts.Reconnects
		s.WriteTimeouts = ts.WriteTimeouts
		// Prefer the transport's write-error tally when it tracks one.
		if ts.WriteErrors > s.TxErrors {
			s.TxErrors = ts.WriteErrors
		}
	}

	// Gauges.
	l.slotMu.Lock()
	s.SlotsActive = int64(len(l.slotByAd))
	l.slotMu.Unlock()
	s.RxQueueLen, s.RxQueueCap = int64(len(l.rxCh)), int64(cap(l.rxCh))
	s.ObsQueueLen, s.ObsQueueCap = int64(len(l.obsCh)), int64(cap(l.obsCh))
	s.SyncQueueLen, s.SyncQueueCap = int64(len(l.syncCh)), int64(cap(l.syncCh))
	s.TxQueueLen, s.TxQueueCap = int64(len(l.txCh)), int64(cap(l.txCh))
	return s
}

func NewLocoNetSerial(device string, baudrate int) (*LocoNet, error) {
	ln := newLocoNetBase()
	t, err := newLnSerialTransport(device, baudrate, ln.rxCh)
	if err != nil {
		return nil, err
	}
	ln.t = t
	go ln.txLoop()
	go ln.dispatch()
	go ln.keepaliveLoop()
	return ln, nil
}

func NewLocoNetTCP(host string, port uint16) (*LocoNet, error) {
	ln := newLocoNetBase()
	t, err := newLnTCPASCIITransport(host, port, ln.rxCh)
	if err != nil {
		return nil, err
	}
	ln.t = t
	go ln.txLoop()
	go ln.dispatch()
	go ln.keepaliveLoop()
	return ln, nil
}

// NewLocoNetTCPBinary connects to a gateway that speaks RAW LocoNet bytes
// over TCP (no ASCII SEND/RECEIVE framing) — the protocol of RocRail's
// lbtcp client. Use this when a LoconetOverTcp/LbServer (ASCII) connection
// dials successfully but every request times out because the peer streams
// binary LocoNet instead of `RECEIVE` lines.
func NewLocoNetTCPBinary(host string, port uint16) (*LocoNet, error) {
	ln := newLocoNetBase()
	t, err := newLnTCPBinaryTransport(host, port, ln.rxCh)
	if err != nil {
		return nil, err
	}
	ln.t = t
	go ln.txLoop()
	go ln.dispatch()
	go ln.keepaliveLoop()
	return ln, nil
}

// SetTimeout adjusts the request/response deadline for LocoNet operations.
func (l *LocoNet) SetTimeout(d time.Duration) {
	if d > 0 {
		l.timeout = d
	}
}

func (l *LocoNet) CleanUp() error {
	// Release all cached slots before closing the transport so the command
	// station knows BigFred no longer owns any locomotives. A physical FRED
	// can then claim them immediately after BigFred disconnects.
	l.releaseAllSlots()
	select {
	case <-l.stop:
	default:
		close(l.stop)
	}
	return l.t.Close()
}

// releaseAllSlots sends OPC_SLOT_STAT1 COMMON for every currently cached
// slot, atomically clears the cache, and logs the result.
// Fire-and-forget: OPC_SLOT_STAT1 does not produce a reply.
func (l *LocoNet) releaseAllSlots() {
	type pair struct {
		addr  LocoAddr
		slot  byte
		stat1 byte
	}
	l.slotMu.Lock()
	if len(l.slotByAd) == 0 {
		l.slotMu.Unlock()
		return
	}
	pairs := make([]pair, 0, len(l.slotByAd))
	l.stateMu.Lock()
	for addr, slot := range l.slotByAd {
		pairs = append(pairs, pair{addr: addr, slot: slot, stat1: l.stat1ByA[addr]})
	}
	l.stateMu.Unlock()
	l.slotByAd = make(map[LocoAddr]byte)
	l.slotAddr = make(map[byte]LocoAddr)
	l.slotMu.Unlock()

	l.reqMu.Lock()
	defer l.reqMu.Unlock()
	for _, p := range pairs {
		// Stop before releasing: ReleaseSlot/COMMON leaves the loco at its
		// current speed on the track (LocoNet spec). On daemon shutdown we
		// must not leave rolling stock moving after the process exits.
		if err := l.sendLocked(lnBuildSetSpeed(p.slot, 0)); err != nil {
			logrus.WithError(err).Debugf("loconet: releaseAllSlots stop slot %d addr %d", p.slot, p.addr)
		}
		common := lnSlotToCommon(p.stat1)
		if err := l.sendLocked(lnBuildSlotStat1(p.slot, common)); err != nil {
			logrus.WithError(err).Debugf("loconet: releaseAllSlots: slot %d addr %d", p.slot, p.addr)
			continue
		}
		l.trackCsSlotStatus(p.slot, common)
		logrus.Debugf("loconet: released slot %d (addr %d) on shutdown", p.slot, p.addr)
	}
}

// ObserveStates implements StateObserver: LocoNet is a shared bus, so
// every speed/direction/function packet — including those authored by
// an external throttle — is visible to the daemon and surfaces here.
func (l *LocoNet) ObserveStates() <-chan LocoObservation {
	return l.obsCh
}

// dispatch is the single owner of rxCh. It demultiplexes the shared bus
// into two consumers: the observation pipeline (always) and the
// request/response waiters (only while syncActive).
func (l *LocoNet) dispatch() {
	for {
		select {
		case <-l.stop:
			return
		case pkt, ok := <-l.rxCh:
			if !ok {
				return
			}
			if logrus.IsLevelEnabled(logrus.DebugLevel) {
				logrus.Debugf("loconet RX: % X", []byte(pkt))
			}
			if len(pkt) > 0 {
				l.metrics.countRx(pkt[0])
			}
			l.observe(pkt)
			if l.syncActive.Load() && lnIsSyncReply(pkt) {
				select {
				case l.syncCh <- pkt:
				default:
					l.metrics.incr(&l.metrics.syncDropped)
				}
			}
		}
	}
}

// observe parses a bus packet, refreshes the local caches and emits a
// LocoObservation for the change. Slot-keyed packets (SPD/DIRF/SND) are
// attributed via the reverse slot→addr map populated from slot reads.
// applySlotData refreshes BigFred's per-loco cache (slot mapping, direction,
// the F0..F8 function groups and speed) from an OPC_SL_RD_DATA frame. It is the
// authoritative seed and is therefore called only from the request paths (slot
// acquire / query / dispatch) — i.e. when WE asked the command station for the
// slot — so a subscription deterministically refreshes the cache straight from
// the reply. The passive observer (observe) deliberately does NOT call this: it
// must not let an echo or a stale broadcast overwrite the DIRF/SND function
// bytes we command. It intentionally does not emit a LocoObservation: observe()
// owns fan-out so upper layers are notified exactly once.
func (l *LocoNet) applySlotData(sd lnSlotData) {
	l.setSlot(sd.Addr, sd.Slot)
	l.setDirf(sd.Addr, sd.DirF)
	l.setSnd(sd.Addr, sd.Snd)
	l.setSpd(sd.Addr, sd.Speed)
	l.setStat1(sd.Addr, sd.Stat1)
}

// trackCsSlotStatus records a command-station slot status change for telemetry.
// It counts bus-wide transitions to IN_USE (occupied) and away from IN_USE
// (released), independent of locomotive address. Repeated observations of the
// same status are ignored.
func (l *LocoNet) trackCsSlotStatus(slot byte, stat1 byte) {
	if slot >= 120 {
		return
	}
	sta := stat1 & lnSLOT_STA_MASK
	prev := byte(l.csSlotStaLast[slot].Swap(uint32(sta)))
	switch {
	case prev == csSlotStaUnknown:
		if sta == lnSLOT_IN_USE {
			l.metrics.incr(&l.metrics.csSlotOccupied)
		}
	case prev != lnSLOT_IN_USE && sta == lnSLOT_IN_USE:
		l.metrics.incr(&l.metrics.csSlotOccupied)
	case prev == lnSLOT_IN_USE && sta != lnSLOT_IN_USE:
		l.metrics.incr(&l.metrics.csSlotReleased)
		// If the master purged/reassigned one of OUR owned slots, notify the
		// observer so the leaser can drop the stale lease. External slots are
		// not mapped to an addr here, so they are not reported.
		if addr, ok := l.slotAddrLocked(slot); ok {
			l.emitSlotReleased(addr)
		}
	}
}

// SetSlotObserver attaches a SlotObserver that receives IN_USE/release events
// for slots the driver owns. nil clears the observer. Safe to call before the
// bus loops start.
func (l *LocoNet) SetSlotObserver(obs SlotObserver) {
	if obs == nil {
		l.slotObs.Store(nil)
		return
	}
	l.slotObs.Store(&obs)
}

func (l *LocoNet) slotAddrLocked(slot byte) (LocoAddr, bool) {
	l.slotMu.Lock()
	defer l.slotMu.Unlock()
	addr, ok := l.slotAddr[slot]
	return addr, ok
}

// emitSlotInUse notifies the observer (if any) that addr's slot is IN_USE.
// Called after a successful acquire. Non-blocking: the observer must not hold
// driver locks on the callback path.
func (l *LocoNet) emitSlotInUse(addr LocoAddr) {
	if addr == 0 {
		return
	}
	if p := l.slotObs.Load(); p != nil {
		(*p).OnSlotInUse(addr)
	}
}

// emitSlotReleased notifies the observer (if any) that addr's slot is no longer
// IN_USE (released to COMMON, purged, or reassigned).
func (l *LocoNet) emitSlotReleased(addr LocoAddr) {
	if addr == 0 {
		return
	}
	if p := l.slotObs.Load(); p != nil {
		(*p).OnSlotReleased(addr)
	}
}

func (l *LocoNet) observe(pkt []byte) {
	if len(pkt) < 2 {
		return
	}
	switch pkt[0] {
	case lnOPC_SL_RD_DATA:
		sd, ok := parseLnSlotData(pkt)
		if !ok {
			return
		}
		// System slots (≥120: programming 0x7C, fast clock 0x7B, …) do
		// not describe a locomotive; skip so a CV reply does not surface
		// as a bogus address-0 observation.
		if sd.Slot >= 120 {
			return
		}
		l.trackCsSlotStatus(sd.Slot, sd.Stat1)
		// Passive observation: refresh the slot mapping and last-seen speed
		// ONLY for locos that BigFred currently owns. Unconditionally calling
		// setSlot here would re-adopt a slot that BigFred explicitly released
		// (clearSlot): the keepalive loop would then find the address in
		// slotByAd and keep refreshing the track with the stale speed from
		// the command-station broadcast, preventing the loco from stopping.
		// The authoritative seed path (acquireSlotFreshLocked / applySlotData)
		// still populates the cache when BigFred itself requests the slot.
		if prevSlot, owned := l.getSlot(sd.Addr); owned {
			// Refresh the slot number when the command station moved this loco
			// (purge + reassignment). Never adopt an unowned slot here — that
			// would resurrect keepalive traffic after ReleaseSlot. Do not copy
			// sd.Speed into the spd cache: BigFred is the authoritative
			// throttle and keepalive must re-send our last command, not a
			// stale broadcast from the command station (which would undo
			// teardown or fight an in-flight SetSpeed).
			if sd.Slot != prevSlot {
				l.setSlot(sd.Addr, sd.Slot)
			}
			// Keep STAT1 fresh so ReleaseSlot can preserve decoder-type bits.
			l.setStat1(sd.Addr, sd.Stat1)
		}
		fm, fb := lnSlotFnObservation(sd.DirF, sd.Snd)
		l.emit(LocoObservation{
			Addr:         sd.Addr,
			HasSpeed:     true,
			Speed:        sd.Speed,
			HasForward:   true,
			Forward:      (sd.DirF & 0x20) != 0,
			FunctionMask: fm,
			FunctionBits: fb,
		})
	case lnOPC_LOCO_SPD:
		if len(pkt) < 4 {
			return
		}
		addr, ok := l.slotToAddr(pkt[1])
		if !ok {
			return
		}
		l.setSpd(addr, pkt[2])
		l.emit(LocoObservation{Addr: addr, HasSpeed: true, Speed: pkt[2]})
	case lnOPC_LOCO_DIRF:
		if len(pkt) < 4 {
			return
		}
		addr, ok := l.slotToAddr(pkt[1])
		if !ok {
			return
		}
		dirf := pkt[2]
		// Authoritative-send model: surface the observation for the UI feed
		// but do not overwrite the DIRF byte we command from passive bus
		// traffic (echo / external throttle / stale broadcast).
		fm, fb := lnDirfFnObservation(dirf)
		l.emit(LocoObservation{
			Addr:         addr,
			HasForward:   true,
			Forward:      (dirf & 0x20) != 0,
			FunctionMask: fm,
			FunctionBits: fb,
		})
	case lnOPC_LOCO_SND:
		if len(pkt) < 4 {
			return
		}
		addr, ok := l.slotToAddr(pkt[1])
		if !ok {
			return
		}
		snd := pkt[2]
		// Authoritative-send model: surface the observation but do not adopt
		// the SND byte we command from passive bus traffic.
		fm, fb := lnSndFnObservation(snd)
		l.emit(LocoObservation{Addr: addr, FunctionMask: fm, FunctionBits: fb})
	case lnOPC_IMM_PACKET:
		// Extended functions (F9..F28) ride immediate DCC packets,
		// addressed by loco number rather than slot. Decode them so an
		// external throttle's F9+ changes are observed too.
		dcc := decodeImmDccPacket(pkt)
		if dcc == nil {
			return
		}
		addr, fns, ok := dccPacketFunctions(dcc)
		if !ok {
			return
		}
		l.mergeExtFn(addr, fns)
		fm, fb := fnMapToObservation(fns)
		l.emit(LocoObservation{Addr: addr, FunctionMask: fm, FunctionBits: fb})
	case lnOPC_SLOT_STAT1:
		if len(pkt) < 3 {
			return
		}
		l.trackCsSlotStatus(pkt[1], pkt[2])
	}
}

func (l *LocoNet) emit(obs LocoObservation) {
	if l.obsCoalesce != nil {
		l.obsCoalesce.submit(obs)
		return
	}
	select {
	case l.obsCh <- obs:
	default:
		l.metrics.incr(&l.metrics.obsDropped)
		logrus.Debug("loconet: observation channel full, dropping update")
	}
}

// beginSync routes subsequent bus packets to the request/response
// waiter. Callers hold reqMu, so only one sequence runs at a time.
func (l *LocoNet) beginSync() {
	l.drainSync()
	l.syncActive.Store(true)
}

func (l *LocoNet) endSync() {
	l.syncActive.Store(false)
	l.drainSync()
}

func (l *LocoNet) drainSync() {
	for {
		select {
		case <-l.syncCh:
		default:
			return
		}
	}
}

// progTimeout is the default per-attempt deadline for a service-mode
// programming task; decoder service-mode reads can take a few seconds.
const progTimeout = 15 * time.Second

// ReadCV reads a single CV from a decoder on the programming (service)
// track via the programming slot (0x7C). POM reads over LocoNet need
// RailCom feedback and are not supported here.
func (l *LocoNet) ReadCV(mode Mode, lcv LocoCV, options ...ctxOptions) (int, error) {
	if mode != ProgrammingTrackMode {
		return 0, fmt.Errorf("ReadCV: LocoNet supports CV read only on the programming track (mode %q); POM read requires RailCom", mode)
	}
	ctx := RequestContext{timeout: progTimeout, retries: 0}
	applyMethodsToCtx(&ctx, options)
	cv0 := lcv.Cv.Translate() // 0-based CV address

	l.reqMu.Lock()
	defer l.reqMu.Unlock()
	l.beginSync()
	defer l.endSync()

	var lastErr error
	for i := 0; i <= int(ctx.retries); i++ {
		val, err := l.readCVLocked(cv0, ctx.timeout)
		if err == nil {
			return val, nil
		}
		logrus.Debugf("ReadCV: attempt %d/%d failed: %v", i+1, int(ctx.retries)+1, err)
		lastErr = err
	}
	return 0, lastErr
}

// WriteCV writes a single CV to a decoder on the programming (service)
// track via the programming slot (0x7C). Added alongside ReadCV so the
// LocoNet CV surface is symmetric; POM writes are not supported here.
func (l *LocoNet) WriteCV(mode Mode, lcv LocoCV, options ...ctxOptions) error {
	if mode != ProgrammingTrackMode {
		return fmt.Errorf("WriteCV: LocoNet supports CV write only on the programming track (mode %q)", mode)
	}
	ctx := RequestContext{timeout: progTimeout, retries: 0}
	applyMethodsToCtx(&ctx, options)
	cv0 := lcv.Cv.Translate()
	val := byte(lcv.Cv.Value)

	l.reqMu.Lock()
	defer l.reqMu.Unlock()
	l.beginSync()
	defer l.endSync()

	var lastErr error
	for i := 0; i <= int(ctx.retries); i++ {
		if err := l.writeCVLocked(cv0, val, ctx.timeout); err != nil {
			logrus.Debugf("WriteCV: attempt %d/%d failed: %v", i+1, int(ctx.retries)+1, err)
			lastErr = err
			continue
		}
		lastErr = nil
		break
	}
	if lastErr != nil {
		return lastErr
	}

	if ctx.verify {
		got, err := l.readCVLocked(cv0, ctx.timeout)
		if err != nil {
			return fmt.Errorf("WriteCV: cannot verify written value: %w", err)
		}
		if byte(got) != val {
			return fmt.Errorf("WriteCV: verify mismatch (wrote %d, read %d)", val, got)
		}
	}
	return nil
}

// readCVLocked runs one service-mode direct-byte read. Caller holds reqMu
// and has begun a sync window.
func (l *LocoNet) readCVLocked(cv0 uint16, timeout time.Duration) (int, error) {
	if err := l.sendLocked(lnBuildProgTask(lnPCMD_READ_DIRECT, cv0, 0)); err != nil {
		return 0, err
	}
	rep, err := l.awaitProgReplyLocked(time.Now().Add(timeout), false)
	if err != nil {
		return 0, err
	}
	if err := lnProgStatusError(rep.PStat); err != nil {
		return 0, err
	}
	return int(rep.Value), nil
}

// writeCVLocked runs one service-mode direct-byte write. Caller holds
// reqMu and has begun a sync window.
func (l *LocoNet) writeCVLocked(cv0 uint16, val byte, timeout time.Duration) error {
	if err := l.sendLocked(lnBuildProgTask(lnPCMD_WRITE_DIRECT, cv0, val)); err != nil {
		return err
	}
	// A write may be accepted "blind" (LACK 0x40) with no slot reply.
	rep, err := l.awaitProgReplyLocked(time.Now().Add(timeout), true)
	if err != nil {
		return err
	}
	return lnProgStatusError(rep.PStat)
}

// awaitProgReplyLocked consumes bus packets until the programming task
// resolves: an error LACK, a "blind" acceptance (success only when
// allowBlind), or the final OPC_SL_RD_DATA result from slot 0x7C.
func (l *LocoNet) awaitProgReplyLocked(deadline time.Time, allowBlind bool) (lnProgReply, error) {
	for time.Now().Before(deadline) {
		pkt, err := l.readPacketUntil(deadline)
		if err != nil {
			return lnProgReply{}, err
		}
		// Immediate long-acknowledge for the programmer task.
		if len(pkt) >= 4 && pkt[0] == lnOPC_LONG_ACK && pkt[1] == 0x7F {
			switch pkt[2] {
			case 0x01: // accepted; result follows in an E7 slot read
				continue
			case 0x40: // accepted blind; no slot reply will come
				if allowBlind {
					return lnProgReply{}, nil
				}
				return lnProgReply{}, errors.New("command station accepted programming task 'blind' (no result returned)")
			case 0x00:
				return lnProgReply{}, errors.New("programmer busy, task aborted")
			case 0x7F:
				return lnProgReply{}, errors.New("programming not implemented by command station")
			default:
				return lnProgReply{}, fmt.Errorf("unexpected programmer LACK code 0x%02X", pkt[2])
			}
		}
		if rep, ok := parseLnProgReply(pkt); ok {
			return rep, nil
		}
	}
	return lnProgReply{}, errors.New("timeout waiting for programming reply")
}

// lnIsSyncReply reports whether pkt may answer a request/response sequence
// (slot read or long-acknowledge). Speed/function traffic is never a reply.
func lnIsSyncReply(pkt []byte) bool {
	if len(pkt) < 1 {
		return false
	}
	switch pkt[0] {
	case lnOPC_SL_RD_DATA, lnOPC_LONG_ACK:
		return true
	default:
		return false
	}
}

// lnProgStatusError maps a programming-slot PSTAT byte to an error.
func lnProgStatusError(pstat byte) error {
	switch {
	case pstat == 0:
		return nil
	case pstat&lnPSTAT_NO_DECODER != 0:
		return errors.New("no decoder detected on the programming track")
	case pstat&lnPSTAT_READ_FAIL != 0:
		return errors.New("decoder read failed (no acknowledge)")
	case pstat&lnPSTAT_WRITE_FAIL != 0:
		return errors.New("decoder write failed (no acknowledge)")
	case pstat&lnPSTAT_USER_ABORTED != 0:
		return errors.New("programming task aborted")
	default:
		return fmt.Errorf("programming failed (PSTAT=0x%02X)", pstat)
	}
}

func (l *LocoNet) SendFn(mode Mode, addr LocoAddr, num FuncNum, toggle bool) error {
	if mode != MainTrackMode {
		return fmt.Errorf("SendFn: unsupported mode '%s' in LocoNet", mode)
	}
	fn := int(num)
	if fn < 0 || fn > 28 {
		// F0..F8 ride the slot; F9..F28 ride immediate DCC packets.
		// F29+ would need the 0xD8.. groups / binary-state packets,
		// which are not implemented here.
		return fmt.Errorf("SendFn: unsupported function number %d (LocoNet driver supports F0-F28)", fn)
	}

	// F9..F28 are not stored in the slot; send them as an immediate DCC
	// function-group packet addressed by loco number (no slot needed).
	if fn >= 9 {
		return l.sendExtFn(addr, fn, toggle)
	}

	slot, err := l.acquireSlot(addr)
	if err != nil {
		return err
	}

	// Trust the cached DIRF/SND state. The driver keeps it current from our
	// own sends and from authoritative slot reads on acquire, so the previous
	// per-call OPC_RQ_SL_DATA round trip was redundant — and, held under the
	// global request lock, it serialized every other locomotive behind a
	// ~15 ms wait on each function toggle. Dropping it is the single biggest
	// fleet-latency win.
	//
	// Serialize the read-modify-write of the shared function byte for this
	// loco under a per-address lock: F0..F4 (DIRF) and F5..F8 (SND) are each a
	// single byte, so a concurrent toggle (e.g. the dead-man horn pulse) or a
	// direction change must not interleave the read of the old byte with our
	// store and transmit a stale bit (which made F1 flicker on an F2/F3 press).
	// Lock order is fn-lock → txMu (sendLocked takes txMu); writeSpeed honours
	// the same order.
	fl := l.addrFnLock(addr)
	fl.Lock()
	defer fl.Unlock()

	if fn <= 4 {
		dirf := setFnInDirf(l.getDirf(addr), fn, toggle)
		if err := l.sendLocked(lnBuildSetDirF(slot, dirf)); err != nil {
			return err
		}
		l.setDirf(addr, dirf)
		return nil
	}

	snd := setFnInSnd(l.getSnd(addr), fn, toggle)
	if err := l.sendLocked(lnBuildSetSnd(slot, snd)); err != nil {
		return err
	}
	l.setSnd(addr, snd)
	return nil
}

// addrFnLock returns the per-address mutex that serializes the DIRF/SND
// read-modify-write for one locomotive (see SendFn / writeSpeed).
func (l *LocoNet) addrFnLock(addr LocoAddr) *sync.Mutex {
	l.fnTxMu.Lock()
	defer l.fnTxMu.Unlock()
	m := l.fnTxLocks[addr]
	if m == nil {
		m = &sync.Mutex{}
		l.fnTxLocks[addr] = m
	}
	return m
}

func (l *LocoNet) ListFunctions(addr LocoAddr) ([]int, error) {
	l.reqMu.Lock()
	defer l.reqMu.Unlock()
	l.beginSync()
	defer l.endSync()

	slot, err := l.ensureSlotLocked(addr)
	if err != nil {
		return nil, err
	}
	sd, err := l.querySlotLocked(slot, addr)
	if err != nil {
		return nil, err
	}

	var on []int
	for fn := 0; fn <= 8; fn++ {
		if fn <= 4 {
			if getFnFromDirf(sd.DirF, fn) {
				on = append(on, fn)
			}
		} else {
			if getFnFromSnd(sd.Snd, fn) {
				on = append(on, fn)
			}
		}
	}
	// F9..F28 are not in the slot; report them from the cache (the only
	// state we have, fed by our own sends and observed bus packets).
	extBits := l.getExtFn(addr)
	for fn := 9; fn <= 28; fn++ {
		if extBits&(1<<uint(fn)) != 0 {
			on = append(on, fn)
		}
	}
	return on, nil
}

// SetSpeed sets speed and direction. On the hot path (slot already cached) it
// takes no request/response lock: it paces a single OPC_LOCO_SPD frame — plus
// OPC_LOCO_DIRF only when the direction bit actually changes — so 20+ locos can
// be driven without serializing behind each other. Acquiring a slot for a
// not-yet-seen address still goes through the request/response path once.
func (l *LocoNet) SetSpeed(addr LocoAddr, speed uint8, forward bool, speedSteps uint8) error {
	lnSpeed, err := scaleToLnSpeed(speed, speedSteps)
	if err != nil {
		return err
	}
	slot, err := l.acquireSlot(addr)
	if err != nil {
		return err
	}
	gen := l.nextSpeedGen(addr)
	return l.writeSpeed(addr, slot, lnSpeed, forward, gen)
}

// writeSpeed paces and writes the speed frame, coalescing superseded updates:
// if a newer SetSpeed for this address arrived while we waited for the bus, the
// stale frame is dropped instead of wasting a scarce transmission slot. The
// direction frame is sent only when the DIR bit changed (it rarely does during
// a throttle sweep), halving speed-related bus traffic.
func (l *LocoNet) writeSpeed(addr LocoAddr, slot, lnSpeed byte, forward bool, gen uint64) error {
	// The direction change below is a read-modify-write of the shared DIRF
	// byte, so take the per-address function lock first (consistent order with
	// SendFn: fn-lock → txEnqueue) to avoid losing a concurrent function toggle.
	fl := l.addrFnLock(addr)
	fl.Lock()
	defer fl.Unlock()

	if l.currentSpeedGen(addr) != gen {
		l.metrics.incr(&l.metrics.txCoalesced)
		return ErrSpeedSuperseded
	}
	spdPkt := lnBuildSetSpeed(slot, lnSpeed)
	if err := l.txEnqueue(spdPkt, lnTxPriorityNormal); err != nil {
		return err
	}
	l.setSpd(addr, lnSpeed)
	l.markAcquired(addr)

	dirf := l.getDirf(addr)
	want := dirf
	if forward {
		want |= 0x20
	} else {
		want &^= 0x20
	}
	if want != dirf {
		if err := l.txEnqueue(lnBuildSetDirF(slot, want), lnTxPriorityNormal); err != nil {
			return err
		}
		l.setDirf(addr, want)
	}
	return nil
}

// acquireSlot returns the slot for addr. A cached slot is trusted only while
// recentlyAcquired reports a fresh command-station round trip; otherwise the
// mapping is revalidated so a stale slot number (e.g. after a purge/reassign
// or a buggy external client) cannot silently swallow drive commands.
func (l *LocoNet) acquireSlot(addr LocoAddr) (byte, error) {
	slot, _, err := l.acquireSlotWithHeld(addr)
	return slot, err
}

// acquireSlotWithHeld is like acquireSlot but reports whether the slot was
// already IN_USE (or recently validated in our cache) before this call.
func (l *LocoNet) acquireSlotWithHeld(addr LocoAddr) (slot byte, heldBefore bool, err error) {
	if slot, ok := l.getSlot(addr); ok && l.recentlyAcquired(addr) {
		return slot, true, nil
	}
	slot, heldBefore, err = l.validateSlot(addr)
	if err == nil {
		l.emitSlotInUse(addr)
	}
	return slot, heldBefore, err
}

// acquireSlotWithHeldNoObserve is acquireSlotWithHeld without the SlotObserver
// notification. EmergencyStop uses it so a transient stop of a loco with no
// BigFred lease does not create a synthetic "external" lease in the leaser.
func (l *LocoNet) acquireSlotWithHeldNoObserve(addr LocoAddr) (slot byte, heldBefore bool, err error) {
	if slot, ok := l.getSlot(addr); ok && l.recentlyAcquired(addr) {
		return slot, true, nil
	}
	return l.validateSlot(addr)
}

func (l *LocoNet) GetSpeed(addr LocoAddr) (speed uint8, forward bool, err error) {
	l.reqMu.Lock()
	defer l.reqMu.Unlock()
	l.beginSync()
	defer l.endSync()

	slot, err := l.ensureSlotLocked(addr)
	if err != nil {
		return 0, false, err
	}
	sd, err := l.querySlotLocked(slot, addr)
	if err != nil {
		return 0, false, err
	}
	forward = (sd.DirF & 0x20) != 0
	return uint8(sd.Speed), forward, nil
}

// sendLocked transmits one frame via the async writer (normal priority).
func (l *LocoNet) sendLocked(pkt []byte) error {
	return l.txEnqueue(pkt, lnTxPriorityNormal)
}

// SetTrackPower implements TrackPowerController via OPC_GPON / OPC_GPOFF.
func (l *LocoNet) SetTrackPower(on bool) error {
	if l == nil {
		return ErrTrackPowerUnsupported
	}
	return l.txEnqueue(lnBuildGlobalPower(on), lnTxPriorityNormal)
}

// pace blocks until the minimum inter-frame gap for pkt has elapsed since the
// last transmission. Only txLoop calls this.
func (l *LocoNet) pace(pkt []byte) {
	gap := l.frameGap(pkt)
	if gap <= 0 {
		return
	}
	if wait := gap - time.Since(l.lastTxAt); wait > 0 {
		l.metrics.addPaceWait(int64(wait))
		time.Sleep(wait)
	}
}

// frameGap returns how long the bus needs to drain pkt before the next frame.
func (l *LocoNet) frameGap(pkt []byte) time.Duration {
	if l.minTxGap <= 0 {
		return 0
	}
	// 10 bit-times per byte at 60µs, plus mandatory CD backoff ~1.2 ms.
	var drain time.Duration
	if len(pkt) > 0 {
		drain = time.Duration(10*len(pkt))*60*time.Microsecond + 1200*time.Microsecond
	}
	if drain < l.minTxGap {
		return l.minTxGap
	}
	return drain
}

// writeRaw validates and writes one frame to the transport, advancing the
// pacing clock. Only txLoop calls this.
func (l *LocoNet) writeRaw(pkt []byte) error {
	if !lnChecksumOK(pkt) {
		return fmt.Errorf("refusing to send packet with invalid checksum: % X", pkt)
	}
	if logrus.IsLevelEnabled(logrus.DebugLevel) {
		logrus.Debugf("loconet TX: % X", pkt)
	}
	err := l.t.WritePacket(pkt)
	l.lastTxAt = time.Now()
	if err != nil {
		l.metrics.incr(&l.metrics.txErrors)
	} else if len(pkt) > 0 {
		l.metrics.countTx(pkt[0], len(pkt))
	}
	return err
}

func (l *LocoNet) ensureSlotLocked(addr LocoAddr) (byte, error) {
	if slot, ok := l.getSlot(addr); ok {
		return slot, nil
	}
	slot, _, err := l.acquireSlotFreshLocked(addr)
	return slot, err
}

// SetAllocatePhysicalSlots implements PhysicalSlotAllocator. When enabled
// (default), BigFred allocates slots like a physical FRED (PE 1.0 exclusive
// IN_USE). When disabled, drive commands may piggyback on slots already
// IN_USE by another throttle.
func (l *LocoNet) SetAllocatePhysicalSlots(enabled bool) {
	l.allocatePhysical.Store(enabled)
}

// ownsSlot reports whether BigFred already tracks addr on the given slot
// number (a prior successful claim in this process).
func (l *LocoNet) ownsSlot(addr LocoAddr, slot byte) bool {
	got, ok := l.getSlot(addr)
	return ok && got == slot
}

// acquireSlotFreshLocked always queries the command station for addr's slot,
// refreshes the cache (re-mapping if the slot number changed) and asserts
// IN_USE via NULL MOVE. Unlike ensureSlotLocked it ignores the cache, so it
// reclaims a slot the command station purged to COMMON or reassigned to another
// loco while BigFred was idle. NULL MOVE runs only when the slot is not already
// IN_USE, so an active physical throttle (FRED) currently owning the slot is
// never stolen.
//
// When allocatePhysicalSlots is enabled and the slot is already IN_USE by
// another throttle, returns ErrSlotInUse without adopting the slot into the
// ownership cache. The slot number is still returned so EmergencyStop can
// write a stop frame without claiming ownership.
//
// Caller holds reqMu and has called beginSync.
// wasAlreadyInUse reports whether the slot was IN_USE before this call (no NULL
// MOVE was needed).
func (l *LocoNet) acquireSlotFreshLocked(addr LocoAddr) (slot byte, wasAlreadyInUse bool, err error) {
	if _, owned := l.getSlot(addr); !owned {
		l.slotMu.Lock()
		n := len(l.slotByAd)
		l.slotMu.Unlock()
		if n >= lnMaxOwnedSlots {
			return 0, false, fmt.Errorf("loconet: owned-slot cap reached (%d); release a slot first", n)
		}
	}
	// Request slot allocation/lookup.
	if err := l.sendLocked(lnBuildLocoAdr(addr)); err != nil {
		return 0, false, err
	}

	deadline := time.Now().Add(l.slotReplyTimeout)
	for time.Now().Before(deadline) {
		pkt, err := l.readPacketUntil(deadline)
		if err != nil {
			return 0, false, err
		}
		if len(pkt) >= 4 && pkt[0] == lnOPC_LONG_ACK && pkt[1] == (lnOPC_LOCO_ADR&0x7F) {
			if pkt[2] == 0x00 {
				l.metrics.incr(&l.metrics.lackRejections)
				return 0, false, ErrNoFreeSlot
			}
		}
		if sd, ok := parseLnSlotData(pkt); ok {
			// Only seed our cache from the E7 that answers THIS address
			// request. Foreign slot reads on a shared bus must not re-adopt
			// released slots or overwrite authoritative DIRF/SND bytes.
			if sd.Addr != addr {
				continue
			}
			wasAlreadyInUse = sd.Stat1&lnSLOT_STA_MASK == lnSLOT_IN_USE
			if l.allocatePhysical.Load() && wasAlreadyInUse && !l.ownsSlot(addr, sd.Slot) {
				// Do not applySlotData: adopting would put the FRED-owned
				// slot into keepalive and claim ownership incorrectly.
				return sd.Slot, true, ErrSlotInUse
			}
			l.applySlotData(sd)
			// Promote slot to IN_USE via NULL MOVE so BigFred is the
			// authoritative throttle. Without this, the slot stays
			// COMMON and the command station may allow another throttle
			// to steal it. Failure is non-fatal: log and continue.
			if !wasAlreadyInUse {
				if err := l.nullMoveLocked(sd.Slot); err != nil {
					logrus.WithError(err).Debugf("loconet: null move for slot %d addr %d skipped", sd.Slot, addr)
				}
			}
			return sd.Slot, wasAlreadyInUse, nil
		}
	}
	return 0, false, errSlotAcquireTimeout(addr, 0)
}

// AcquireSlot makes BigFred the authoritative server-side owner of addr's slot.
// It queries the command station fresh (ignoring the cache) and asserts IN_USE,
// reclaiming a slot the master purged to COMMON or reassigned while the loco was
// idle — so a client leaving and returning to the throttle never silently loses
// control. Slots are owned per-locomotive by the server, independent of any
// session; the drive-permission layer is enforced separately by the caller.
//
// Idempotent and intended for the subscribe path, not the per-tick speed path:
// it performs a command-station round trip. With allocatePhysicalSlots enabled,
// an already-IN_USE slot not owned by BigFred returns ErrSlotInUse. With it
// disabled, an already-IN_USE slot (e.g. held by a physical FRED) is left
// untouched and may be driven (legacy piggyback).
func (l *LocoNet) AcquireSlot(addr LocoAddr) error {
	if addr == 0 {
		return fmt.Errorf("loconet: AcquireSlot invalid addr 0")
	}
	// Debounce reconnect storms: if we already own this slot and validated it
	// within slotRevalidateInterval, skip the fresh LOCO_ADR + slot read +
	// NULL MOVE round trip. The keepalive loop keeps the slot IN_USE in the
	// meantime, so a client that drops and reconnects every few seconds (e.g.
	// a ping-silence dead-man loop) no longer hammers the command station's
	// slot table.
	if l.recentlyAcquired(addr) {
		return nil
	}
	_, _, err := l.validateSlot(addr)
	return err
}

// ForceAcquireSlot always revalidates addr against the command station,
// bypassing the debounce window. Intended for subscribe and command retry.
func (l *LocoNet) ForceAcquireSlot(addr LocoAddr) error {
	if addr == 0 {
		return fmt.Errorf("loconet: ForceAcquireSlot invalid addr 0")
	}
	l.slotMu.Lock()
	delete(l.slotAcquiredAt, addr)
	l.slotMu.Unlock()
	_, _, err := l.validateSlot(addr)
	return err
}

// StealSlot claims addr's slot even when another throttle already holds it
// IN_USE. It stops the loco on the wire, writes STAT1 to COMMON (preserving
// decoder-type bits), then NULL MOVEs so BigFred becomes the owner. This is
// the explicit user-confirmed takeover path; normal AcquireSlot still refuses
// foreign IN_USE when allocatePhysicalSlots is enabled.
func (l *LocoNet) StealSlot(addr LocoAddr) error {
	if addr == 0 {
		return fmt.Errorf("loconet: StealSlot invalid addr 0")
	}
	if _, ok := l.getSlot(addr); ok && l.recentlyAcquired(addr) {
		l.emitSlotInUse(addr)
		return nil
	}
	if until := l.slotBreakerUntil.Load(); until > 0 && time.Now().UnixNano() < until {
		return ErrSlotBusUnavailable
	}

	l.reqMu.Lock()
	defer l.reqMu.Unlock()
	l.beginSync()
	defer l.endSync()

	if err := l.stealSlotFreshLocked(addr); err != nil {
		return err
	}
	l.slotBreakerUntil.Store(0)
	l.markAcquired(addr)
	l.metrics.incr(&l.metrics.slotAcquires)
	l.emitSlotInUse(addr)
	return nil
}

// stealSlotFreshLocked queries the master and claims the slot, releasing a
// foreign IN_USE first when needed. Caller holds reqMu and has called beginSync.
func (l *LocoNet) stealSlotFreshLocked(addr LocoAddr) error {
	if _, owned := l.getSlot(addr); !owned {
		l.slotMu.Lock()
		n := len(l.slotByAd)
		l.slotMu.Unlock()
		if n >= lnMaxOwnedSlots {
			return fmt.Errorf("loconet: owned-slot cap reached (%d); release a slot first", n)
		}
	}
	if err := l.sendLocked(lnBuildLocoAdr(addr)); err != nil {
		return err
	}

	deadline := time.Now().Add(l.slotReplyTimeout)
	for time.Now().Before(deadline) {
		pkt, err := l.readPacketUntil(deadline)
		if err != nil {
			return err
		}
		if len(pkt) >= 4 && pkt[0] == lnOPC_LONG_ACK && pkt[1] == (lnOPC_LOCO_ADR&0x7F) {
			if pkt[2] == 0x00 {
				l.metrics.incr(&l.metrics.lackRejections)
				return ErrNoFreeSlot
			}
		}
		sd, ok := parseLnSlotData(pkt)
		if !ok || sd.Addr != addr {
			continue
		}
		sta := sd.Stat1 & lnSLOT_STA_MASK
		if sta == lnSLOT_IN_USE && l.ownsSlot(addr, sd.Slot) {
			l.applySlotData(sd)
			return nil
		}
		if sta == lnSLOT_IN_USE && !l.ownsSlot(addr, sd.Slot) {
			// Stop without adopting, then free the slot for reclaim.
			if err := l.sendLocked(lnBuildSetSpeed(sd.Slot, 1)); err != nil {
				return err
			}
			common := lnSlotToCommon(sd.Stat1)
			if err := l.sendLocked(lnBuildSlotStat1(sd.Slot, common)); err != nil {
				return err
			}
			l.trackCsSlotStatus(sd.Slot, common)
			sd.Stat1 = common
		}
		l.applySlotData(sd)
		if sd.Stat1&lnSLOT_STA_MASK != lnSLOT_IN_USE {
			if err := l.nullMoveLocked(sd.Slot); err != nil {
				return err
			}
		}
		return nil
	}
	return errSlotAcquireTimeout(addr, 0)
}

// validateSlot queries the command station for addr's slot, refreshes the local
// cache from the reply, and asserts IN_USE when needed. Caller must not hold
// reqMu. wasAlreadyInUse is true when the slot was already IN_USE on the master
// (no NULL MOVE was sent).
func (l *LocoNet) validateSlot(addr LocoAddr) (byte, bool, error) {
	if until := l.slotBreakerUntil.Load(); until > 0 && time.Now().UnixNano() < until {
		return 0, false, ErrSlotBusUnavailable
	}

	l.reqMu.Lock()
	defer l.reqMu.Unlock()
	l.beginSync()
	defer l.endSync()

	var lastErr error
	for i := 0; i <= lnSlotAcquireRetries; i++ {
		if i > 0 {
			l.metrics.incr(&l.metrics.slotRetries)
		}
		slot, wasAlreadyInUse, err := l.acquireSlotFreshLocked(addr)
		if err == nil {
			l.slotBreakerUntil.Store(0)
			l.markAcquired(addr)
			l.metrics.incr(&l.metrics.slotAcquires)
			return slot, wasAlreadyInUse, nil
		}
		// Definitive rejections: do not retry or trip the timeout breaker.
		if errors.Is(err, ErrSlotInUse) || errors.Is(err, ErrNoFreeSlot) {
			return slot, wasAlreadyInUse, err
		}
		lastErr = err
		logrus.Debugf("loconet: slot validate attempt %d/%d for addr %d failed: %v",
			i+1, lnSlotAcquireRetries+1, addr, err)
	}
	l.metrics.incr(&l.metrics.slotAcquireFails)
	l.slotBreakerUntil.Store(time.Now().Add(lnSlotBreakerCooldown).UnixNano())
	return 0, false, lastErr
}

// slotRevalidateInterval bounds how long AcquireSlot trusts a previously
// validated slot before doing another command-station round trip. Kept well
// below lnKeepaliveInterval so the keepalive holds the slot IN_USE in between.
const slotRevalidateInterval = 30 * time.Second

// recentlyAcquired reports whether addr's slot is cached and was validated via
// a fresh round trip within slotRevalidateInterval.
func (l *LocoNet) recentlyAcquired(addr LocoAddr) bool {
	l.slotMu.Lock()
	defer l.slotMu.Unlock()
	if _, ok := l.slotByAd[addr]; !ok {
		return false
	}
	at, ok := l.slotAcquiredAt[addr]
	return ok && time.Since(at) < slotRevalidateInterval
}

// markAcquired records that addr's slot was just validated.
func (l *LocoNet) markAcquired(addr LocoAddr) {
	l.slotMu.Lock()
	l.slotAcquiredAt[addr] = time.Now()
	l.slotMu.Unlock()
}

// nullMoveLocked sends OPC_MOVE_SLOTS with src==dst (NULL MOVE), which
// promotes the slot from COMMON or IDLE to IN_USE on the command station.
// Caller must hold reqMu and have called beginSync.
// When the master is silent, the slot is verified with OPC_RQ_SL_DATA.
func (l *LocoNet) nullMoveLocked(slot byte) error {
	if err := l.sendLocked(lnBuildMoveSlots(slot, slot)); err != nil {
		return err
	}
	deadline := time.Now().Add(l.slotReplyTimeout)
	for time.Now().Before(deadline) {
		pkt, err := l.readPacketUntil(deadline)
		if err != nil {
			break
		}
		if sd, ok := parseLnSlotData(pkt); ok && sd.Slot == slot {
			if sd.Stat1&lnSLOT_STA_MASK == lnSLOT_IN_USE {
				l.trackCsSlotStatus(slot, lnSLOT_IN_USE)
				return nil
			}
		}
		// OPC_LONG_ACK for OPC_MOVE_SLOTS: B4 <BA&0x7F=0x3A> <code> <chk>
		if len(pkt) >= 4 && pkt[0] == lnOPC_LONG_ACK && pkt[1] == (lnOPC_MOVE_SLOTS&0x7F) {
			if pkt[2] == 0x00 {
				l.metrics.incr(&l.metrics.lackRejections)
				return fmt.Errorf("loconet: null move rejected by command station for slot %d", slot)
			}
			l.trackCsSlotStatus(slot, lnSLOT_IN_USE)
			return nil // any non-zero code means accepted
		}
	}
	return l.confirmSlotInUseLocked(slot)
}

// confirmSlotInUseLocked queries the slot and insists STAT1 shows IN_USE.
func (l *LocoNet) confirmSlotInUseLocked(slot byte) error {
	sd, err := l.querySlotLocked(slot, 0)
	if err != nil {
		return fmt.Errorf("loconet: null move unconfirmed for slot %d: %w", slot, err)
	}
	if sd.Stat1&lnSLOT_STA_MASK != lnSLOT_IN_USE {
		return fmt.Errorf("loconet: null move left slot %d in state %#x, want IN_USE", slot, sd.Stat1&lnSLOT_STA_MASK)
	}
	l.trackCsSlotStatus(slot, lnSLOT_IN_USE)
	return nil
}

func (l *LocoNet) querySlotLocked(slot byte, addr LocoAddr) (lnSlotData, error) {
	if err := l.sendLocked(lnBuildRqSlotData(slot)); err != nil {
		return lnSlotData{}, err
	}
	deadline := time.Now().Add(l.slotReplyTimeout)
	for time.Now().Before(deadline) {
		pkt, err := l.readPacketUntil(deadline)
		if err != nil {
			return lnSlotData{}, err
		}
		if sd, ok := parseLnSlotData(pkt); ok {
			l.applySlotData(sd)
			if sd.Slot == slot && (addr == 0 || sd.Addr == addr) {
				return sd, nil
			}
		}
	}
	return lnSlotData{}, errSlotAcquireTimeout(addr, slot)
}

func (l *LocoNet) readPacketUntil(deadline time.Time) (lnPacket, error) {
	timeout := time.Until(deadline)
	if timeout <= 0 {
		return nil, errors.New("timeout")
	}
	select {
	case pkt := <-l.syncCh:
		return pkt, nil
	case <-time.After(timeout):
		return nil, errors.New("timeout")
	}
}

func (l *LocoNet) getSlot(addr LocoAddr) (byte, bool) {
	l.slotMu.Lock()
	defer l.slotMu.Unlock()
	slot, ok := l.slotByAd[addr]
	return slot, ok
}

func (l *LocoNet) setSlot(addr LocoAddr, slot byte) {
	if addr == 0 {
		return
	}
	l.slotMu.Lock()
	// If the command station moved this loco to a different slot (purge +
	// reassignment), drop the stale reverse mapping so bus traffic on the old
	// slot is no longer misattributed to this address.
	if prev, ok := l.slotByAd[addr]; ok && prev != slot {
		delete(l.slotAddr, prev)
	}
	l.slotByAd[addr] = slot
	l.slotAddr[slot] = addr
	l.slotMu.Unlock()
}

func (l *LocoNet) clearSlot(addr LocoAddr, slot byte) {
	l.slotMu.Lock()
	delete(l.slotByAd, addr)
	delete(l.slotAddr, slot)
	delete(l.slotAcquiredAt, addr)
	l.slotMu.Unlock()
	l.clearLocoState(addr)
	l.emitSlotReleased(addr)
}

// clearLocoState drops cached speed/direction/function bytes for addr so the
// next acquire cannot read-modify-write stale DIRF/SND left by a released slot.
func (l *LocoNet) clearLocoState(addr LocoAddr) {
	l.stateMu.Lock()
	delete(l.dirfByA, addr)
	delete(l.sndByA, addr)
	delete(l.spdByA, addr)
	delete(l.speedGenByA, addr)
	delete(l.stat1ByA, addr)
	delete(l.extFnByA, addr)
	l.stateMu.Unlock()
	l.fnTxMu.Lock()
	delete(l.fnTxLocks, addr)
	l.fnTxMu.Unlock()
}

// slotToAddr resolves a slot number back to a loco address using the
// reverse map populated whenever a slot read is seen on the bus.
func (l *LocoNet) slotToAddr(slot byte) (LocoAddr, bool) {
	l.slotMu.Lock()
	defer l.slotMu.Unlock()
	addr, ok := l.slotAddr[slot]
	return addr, ok
}

// ReconcileBootSlots scans the command-station slot table and releases
// IN_USE roster slots left from an unclean BigFred shutdown.
func (l *LocoNet) ReconcileBootSlots(roster map[LocoAddr]struct{}) error {
	if len(roster) == 0 {
		return nil
	}
	l.reqMu.Lock()
	defer l.reqMu.Unlock()
	l.beginSync()
	defer l.endSync()

	released := 0
	for slot := byte(1); slot < 120; slot++ {
		sd, err := l.querySlotLocked(slot, 0)
		if err != nil || sd.Addr == 0 {
			continue
		}
		addr := LocoAddr(sd.Addr)
		if _, ok := roster[addr]; !ok {
			continue
		}
		if sd.Stat1&lnSLOT_STA_MASK != lnSLOT_IN_USE {
			continue
		}
		if err := l.sendLocked(lnBuildSetSpeed(slot, 0)); err != nil {
			logrus.WithError(err).Debugf("loconet: boot reconcile stop slot %d addr %d", slot, addr)
		}
		common := lnSlotToCommon(sd.Stat1)
		if err := l.sendLocked(lnBuildSlotStat1(slot, common)); err != nil {
			logrus.WithError(err).Debugf("loconet: boot reconcile release slot %d addr %d", slot, addr)
			continue
		}
		l.trackCsSlotStatus(slot, common)
		l.clearSlot(addr, slot)
		l.metrics.incr(&l.metrics.slotReleases)
		released++
		logrus.WithFields(logrus.Fields{
			"slot": slot,
			"addr": addr,
		}).Info("loconet: boot-released stale IN_USE slot")
	}
	if released > 0 {
		logrus.WithField("count", released).Info("loconet: boot slot reconciliation complete")
	}
	return nil
}

// SlotStatus implements commandstation.SlotReconciler. It reads the slot
// currently mapped to addr and reports whether the command station still holds
// it IN_USE. Returns known=false when the driver has no mapping for addr.
func (l *LocoNet) SlotStatus(addr LocoAddr) (inUse bool, known bool, err error) {
	slot, ok := l.getSlot(addr)
	if !ok {
		return false, false, nil
	}
	l.reqMu.Lock()
	defer l.reqMu.Unlock()
	l.beginSync()
	defer l.endSync()
	sd, err := l.querySlotLocked(slot, 0)
	if err != nil {
		return false, true, err
	}
	if LocoAddr(sd.Addr) != addr {
		return false, true, nil
	}
	return sd.Stat1&lnSLOT_STA_MASK == lnSLOT_IN_USE, true, nil
}

// ReleaseSlot writes OPC_SLOT_STAT1 with COMMON occupancy, preserving
// decoder-type and consist bits from the cached STAT1 (spec §9.1). The
// locomotive continues at its current speed; call SetSpeed first if a
// controlled stop is needed.
func (l *LocoNet) ReleaseSlot(addr LocoAddr) error {
	slot, ok := l.getSlot(addr)
	if !ok {
		return nil // never acquired, nothing to do
	}
	common := lnSlotToCommon(l.getStat1(addr))
	l.reqMu.Lock()
	defer l.reqMu.Unlock()
	if err := l.sendLocked(lnBuildSlotStat1(slot, common)); err != nil {
		return err
	}
	l.trackCsSlotStatus(slot, common)
	l.clearSlot(addr, slot)
	l.metrics.incr(&l.metrics.slotReleases)
	logrus.Debugf("loconet: released slot %d for addr %d (set COMMON %#x)", slot, addr, common)
	return nil
}

// DispatchSlot moves the slot for addr into the LocoNet dispatch slot
// (OPC_MOVE_SLOTS src=slot, dst=0). A physical throttle (e.g. a FRED) can
// then claim it by sending a dispatch GET (OPC_MOVE_SLOTS 0 0).
// The loco should be stopped before calling this to avoid runaway behaviour.
func (l *LocoNet) DispatchSlot(addr LocoAddr) error {
	slot, ok := l.getSlot(addr)
	if !ok {
		return fmt.Errorf("loconet: no tracked slot for addr %d", addr)
	}
	l.reqMu.Lock()
	defer l.reqMu.Unlock()
	l.beginSync()
	defer l.endSync()

	if err := l.sendLocked(lnBuildMoveSlots(slot, 0)); err != nil {
		return err
	}
	deadline := time.Now().Add(l.timeout)
	for time.Now().Before(deadline) {
		pkt, err := l.readPacketUntil(deadline)
		if err != nil {
			return fmt.Errorf("loconet: timeout waiting for dispatch PUT reply (slot %d, addr %d)", slot, addr)
		}
		if sd, ok := parseLnSlotData(pkt); ok && sd.Slot == slot {
			l.clearSlot(addr, slot)
			l.metrics.incr(&l.metrics.slotDispatches)
			logrus.Debugf("loconet: dispatched slot %d for addr %d", slot, addr)
			return nil
		}
		if len(pkt) >= 4 && pkt[0] == lnOPC_LONG_ACK && pkt[1] == (lnOPC_MOVE_SLOTS&0x7F) {
			if pkt[2] == 0x00 {
				l.metrics.incr(&l.metrics.lackRejections)
				return fmt.Errorf("loconet: dispatch PUT rejected for slot %d (addr %d)", slot, addr)
			}
			l.clearSlot(addr, slot)
			l.metrics.incr(&l.metrics.slotDispatches)
			logrus.Debugf("loconet: dispatched slot %d for addr %d (LACK)", slot, addr)
			return nil
		}
	}
	return fmt.Errorf("loconet: timeout waiting for dispatch PUT reply (slot %d, addr %d)", slot, addr)
}

// AcquireDispatched claims the slot currently held in the LocoNet dispatch
// slot (OPC_MOVE_SLOTS src=0, dst=0) and caches it locally.
// Returns (0, nil) when the dispatch slot is empty.
// After acquiring, a NULL MOVE is performed to confirm IN_USE ownership.
func (l *LocoNet) AcquireDispatched() (LocoAddr, error) {
	l.reqMu.Lock()
	defer l.reqMu.Unlock()
	l.beginSync()
	defer l.endSync()

	if err := l.sendLocked(lnBuildMoveSlots(0, 0)); err != nil {
		return 0, err
	}
	deadline := time.Now().Add(l.timeout)
	for time.Now().Before(deadline) {
		pkt, err := l.readPacketUntil(deadline)
		if err != nil {
			return 0, errors.New("loconet: timeout waiting for dispatch GET reply")
		}
		if sd, ok := parseLnSlotData(pkt); ok {
			l.setSlot(sd.Addr, sd.Slot)
			l.setDirf(sd.Addr, sd.DirF)
			l.setSnd(sd.Addr, sd.Snd)
			// Confirm ownership with NULL MOVE (non-fatal if rejected).
			if err := l.nullMoveLocked(sd.Slot); err != nil {
				logrus.WithError(err).Debugf("loconet: null move after dispatch GET failed for slot %d", sd.Slot)
			}
			logrus.Debugf("loconet: acquired dispatched slot %d for addr %d", sd.Slot, sd.Addr)
			return sd.Addr, nil
		}
		// LONG_ACK code 0x00 means the dispatch slot is empty.
		if len(pkt) >= 4 && pkt[0] == lnOPC_LONG_ACK && pkt[1] == (lnOPC_MOVE_SLOTS&0x7F) {
			if pkt[2] == 0x00 {
				return 0, nil // no dispatched slot
			}
		}
	}
	return 0, errors.New("loconet: timeout waiting for dispatch GET reply")
}

func (l *LocoNet) getDirf(addr LocoAddr) byte {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	return l.dirfByA[addr]
}

func (l *LocoNet) setDirf(addr LocoAddr, dirf byte) {
	l.stateMu.Lock()
	l.dirfByA[addr] = dirf
	l.stateMu.Unlock()
}

func (l *LocoNet) getStat1(addr LocoAddr) byte {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	return l.stat1ByA[addr]
}

func (l *LocoNet) setStat1(addr LocoAddr, stat1 byte) {
	if addr == 0 {
		return
	}
	l.stateMu.Lock()
	l.stat1ByA[addr] = stat1
	l.stateMu.Unlock()
}

func (l *LocoNet) getSnd(addr LocoAddr) byte {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	return l.sndByA[addr]
}

func (l *LocoNet) setSnd(addr LocoAddr, snd byte) {
	l.stateMu.Lock()
	l.sndByA[addr] = snd
	l.stateMu.Unlock()
}

func (l *LocoNet) setSpd(addr LocoAddr, spd byte) {
	if addr == 0 {
		return
	}
	l.stateMu.Lock()
	l.spdByA[addr] = spd
	l.stateMu.Unlock()
}

func (l *LocoNet) getSpd(addr LocoAddr) (byte, bool) {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	spd, ok := l.spdByA[addr]
	return spd, ok
}

// nextSpeedGen bumps and returns the per-address SetSpeed generation. Each
// SetSpeed reserves a generation before writing; writeSpeed drops its frame if a
// newer generation has since been reserved (TX coalescing).
func (l *LocoNet) nextSpeedGen(addr LocoAddr) uint64 {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	l.speedGenByA[addr]++
	return l.speedGenByA[addr]
}

func (l *LocoNet) currentSpeedGen(addr LocoAddr) uint64 {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	return l.speedGenByA[addr]
}

// keepaliveLoop re-touches cached slots round-robin so the master does not
// purge them to COMMON after ~200 s of inactivity (spec §4.3).
func (l *LocoNet) keepaliveLoop() {
	if l.keepaliveInterval <= 0 {
		return
	}
	ticker := time.NewTicker(lnKeepaliveTickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-ticker.C:
			l.refreshOneKeepaliveSlot()
		}
	}
}

// refreshOneKeepaliveSlot re-sends one cached locomotive's last speed per tick,
// spreading the purge timer reset across the fleet instead of bursting all slots.
func (l *LocoNet) refreshOneKeepaliveSlot() {
	l.slotMu.Lock()
	addrs := make([]LocoAddr, 0, len(l.slotByAd))
	for addr := range l.slotByAd {
		addrs = append(addrs, addr)
	}
	idx := l.keepaliveIdx
	if len(addrs) > 0 {
		l.keepaliveIdx = (idx + 1) % len(addrs)
	}
	l.slotMu.Unlock()
	if len(addrs) == 0 {
		return
	}
	sort.Slice(addrs, func(i, j int) bool { return addrs[i] < addrs[j] })
	addr := addrs[idx%len(addrs)]
	slot, ok := l.getSlot(addr)
	if !ok {
		return
	}
	spd, ok := l.getSpd(addr)
	if !ok {
		return
	}
	if err := l.txEnqueue(lnBuildSetSpeed(slot, spd), lnTxPriorityLow); err != nil {
		logrus.WithError(err).Debugf("loconet keepalive: slot %d addr %d", slot, addr)
		return
	}
	l.markAcquired(addr)
	l.metrics.incr(&l.metrics.keepaliveRefresh)
}

func (l *LocoNet) getExtFn(addr LocoAddr) uint32 {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	return l.extFnByA[addr]
}

// setExtFn updates a single F9..F28 bit and returns the new full bitmask.
func (l *LocoNet) setExtFn(addr LocoAddr, fn int, on bool) uint32 {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	bits := l.extFnByA[addr]
	if on {
		bits |= 1 << uint(fn)
	} else {
		bits &^= 1 << uint(fn)
	}
	l.extFnByA[addr] = bits
	return bits
}

// mergeExtFn folds an observed set of function bits into the cache.
func (l *LocoNet) mergeExtFn(addr LocoAddr, fns map[int]bool) {
	if addr == 0 || len(fns) == 0 {
		return
	}
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	bits := l.extFnByA[addr]
	for fn, on := range fns {
		if fn < 0 || fn > 31 {
			continue
		}
		if on {
			bits |= 1 << uint(fn)
		} else {
			bits &^= 1 << uint(fn)
		}
	}
	l.extFnByA[addr] = bits
}

// sendExtFn sets one F9..F28 function via an immediate DCC packet. The whole
// group's bitmask is sent, taken from the per-loco cache, so other functions in
// the group are preserved. No slot and no request/response lock are needed; the
// single frame is paced like any other write.
func (l *LocoNet) sendExtFn(addr LocoAddr, fn int, on bool) error {
	bits := l.setExtFn(addr, fn, on)
	dcc, ok := dccFnGroupPacket(addr, fn, bits)
	if !ok {
		return fmt.Errorf("SendFn: no DCC function group for F%d", fn)
	}
	imm, err := lnBuildImmPacket(dcc, lnImmRepeats)
	if err != nil {
		return err
	}
	return l.sendLocked(imm)
}

// Function bit helpers.

func getFnFromDirf(dirf byte, fn int) bool {
	switch fn {
	case 0:
		return (dirf & 0x10) != 0
	case 1:
		return (dirf & 0x01) != 0
	case 2:
		return (dirf & 0x02) != 0
	case 3:
		return (dirf & 0x04) != 0
	case 4:
		return (dirf & 0x08) != 0
	default:
		return false
	}
}

func setFnInDirf(dirf byte, fn int, on bool) byte {
	var mask byte
	switch fn {
	case 0:
		mask = 0x10
	case 1:
		mask = 0x01
	case 2:
		mask = 0x02
	case 3:
		mask = 0x04
	case 4:
		mask = 0x08
	default:
		return dirf
	}
	if on {
		dirf |= mask
	} else {
		dirf &^= mask
	}
	return dirf
}

func getFnFromSnd(snd byte, fn int) bool {
	switch fn {
	case 5:
		return (snd & 0x01) != 0
	case 6:
		return (snd & 0x02) != 0
	case 7:
		return (snd & 0x04) != 0
	case 8:
		return (snd & 0x08) != 0
	default:
		return false
	}
}

func setFnInSnd(snd byte, fn int, on bool) byte {
	var mask byte
	switch fn {
	case 5:
		mask = 0x01
	case 6:
		mask = 0x02
	case 7:
		mask = 0x04
	case 8:
		mask = 0x08
	default:
		return snd
	}
	if on {
		snd |= mask
	} else {
		snd &^= mask
	}
	return snd
}

// --- TCP helper (shared parsing for LoconetOverTcp-style feeds) ---

func lnParseHexBytes(s string) ([]byte, error) {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return nil, fmt.Errorf("empty hex list")
	}
	out := make([]byte, 0, len(fields))
	for _, f := range fields {
		if len(f) == 1 {
			f = "0" + f
		}
		b, err := hexByte(f)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

func hexByte(s string) (byte, error) {
	if len(s) != 2 {
		return 0, fmt.Errorf("invalid hex byte %q", s)
	}
	dec, err := hex.DecodeString(s)
	if err != nil || len(dec) != 1 {
		return 0, fmt.Errorf("invalid hex byte %q", s)
	}
	return dec[0], nil
}
