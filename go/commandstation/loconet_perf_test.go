package commandstation

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// recTransport is a fake lnTransport that records every frame written, so tests
// can assert exactly what hits the bus on the fast path.
type recTransport struct {
	mu   sync.Mutex
	pkts [][]byte
}

func (r *recTransport) WritePacket(pkt []byte) error {
	r.mu.Lock()
	r.pkts = append(r.pkts, append([]byte(nil), pkt...))
	r.mu.Unlock()
	return nil
}

func (r *recTransport) Close() error { return nil }

func (r *recTransport) packets() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][]byte, len(r.pkts))
	copy(out, r.pkts)
	return out
}

func (r *recTransport) reset() {
	r.mu.Lock()
	r.pkts = nil
	r.mu.Unlock()
}

func (r *recTransport) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pkts)
}

// newTestLoconet builds a driver wired to a recording transport with pacing
// disabled, isolating logic (coalescing, DIRF suppression) from timing.
func newTestLoconet() (*LocoNet, *recTransport) {
	l := newLocoNetBase()
	l.minTxGap = 0
	rec := &recTransport{}
	l.t = rec
	go l.txLoop()
	return l, rec
}

func seedCachedSlot(l *LocoNet, addr LocoAddr, slot byte) {
	l.setSlot(addr, slot)
	l.markAcquired(addr)
}

func countOpcode(pkts [][]byte, opc byte) int {
	n := 0
	for _, p := range pkts {
		if len(p) > 0 && p[0] == opc {
			n++
		}
	}
	return n
}

// B4: SetSpeed must send OPC_LOCO_DIRF only when the direction bit changes, not
// on every speed tick.
func TestSetSpeedSuppressesUnchangedDirf(t *testing.T) {
	l, rec := newTestLoconet()
	const addr LocoAddr = 31
	seedCachedSlot(l, addr, 5)

	// First move sets the direction (reverse -> forward): SPD + DIRF.
	if err := l.SetSpeed(addr, 10, true, 128); err != nil {
		t.Fatalf("SetSpeed #1: %v", err)
	}
	pkts := rec.packets()
	if got := countOpcode(pkts, lnOPC_LOCO_SPD); got != 1 {
		t.Fatalf("first move: SPD frames = %d, want 1", got)
	}
	if got := countOpcode(pkts, lnOPC_LOCO_DIRF); got != 1 {
		t.Fatalf("first move: DIRF frames = %d, want 1 (direction changed)", got)
	}

	// Same direction, new speed: SPD only, no DIRF.
	rec.reset()
	if err := l.SetSpeed(addr, 40, true, 128); err != nil {
		t.Fatalf("SetSpeed #2: %v", err)
	}
	pkts = rec.packets()
	if got := countOpcode(pkts, lnOPC_LOCO_SPD); got != 1 {
		t.Fatalf("same direction: SPD frames = %d, want 1", got)
	}
	if got := countOpcode(pkts, lnOPC_LOCO_DIRF); got != 0 {
		t.Fatalf("same direction: DIRF frames = %d, want 0 (direction unchanged)", got)
	}

	// Reversing must send DIRF again.
	rec.reset()
	if err := l.SetSpeed(addr, 40, false, 128); err != nil {
		t.Fatalf("SetSpeed #3: %v", err)
	}
	if got := countOpcode(rec.packets(), lnOPC_LOCO_DIRF); got != 1 {
		t.Fatalf("reversing: DIRF frames = %d, want 1", got)
	}
}

// B3: a speed frame superseded by a newer SetSpeed (higher generation) is
// dropped instead of consuming a transmission slot.
func TestWriteSpeedCoalescesStale(t *testing.T) {
	l, rec := newTestLoconet()
	const addr LocoAddr = 42
	seedCachedSlot(l, addr, 7)
	l.setDirf(addr, 0x20) // forward, so DIRF is never re-sent below

	g1 := l.nextSpeedGen(addr)
	g2 := l.nextSpeedGen(addr) // g2 supersedes g1

	// The stale generation must be dropped.
	if err := l.writeSpeed(addr, 7, 20, true, g1); !errors.Is(err, ErrSpeedSuperseded) {
		t.Fatalf("writeSpeed(stale): %v, want ErrSpeedSuperseded", err)
	}
	if rec.count() != 0 {
		t.Fatalf("stale frame was written (% X), expected coalesced/dropped", rec.packets())
	}

	// The current generation must go out.
	if err := l.writeSpeed(addr, 7, 30, true, g2); err != nil {
		t.Fatalf("writeSpeed(current): %v", err)
	}
	pkts := rec.packets()
	if got := countOpcode(pkts, lnOPC_LOCO_SPD); got != 1 {
		t.Fatalf("current: SPD frames = %d, want 1", got)
	}
	if got := countOpcode(pkts, lnOPC_LOCO_DIRF); got != 0 {
		t.Fatalf("current: DIRF frames = %d, want 0 (direction preseeded)", got)
	}
}

// B1: SendFn for a cached slot trusts the cache and sends a single frame with no
// blocking OPC_RQ_SL_DATA round trip.
func TestSendFnTrustsCacheNoRoundTrip(t *testing.T) {
	l, rec := newTestLoconet()
	const addr LocoAddr = 55
	seedCachedSlot(l, addr, 9)

	// F1 lives in DIRF.
	if err := l.SendFn(MainTrackMode, addr, 1, true); err != nil {
		t.Fatalf("SendFn F1: %v", err)
	}
	pkts := rec.packets()
	if len(pkts) != 1 {
		t.Fatalf("F1: wrote %d frames, want 1 (no slot query): % X", len(pkts), pkts)
	}
	if got := countOpcode(pkts, lnOPC_RQ_SL_DATA); got != 0 {
		t.Fatalf("F1: %d slot-query frames, want 0", got)
	}
	if pkts[0][0] != lnOPC_LOCO_DIRF {
		t.Fatalf("F1: opcode % X, want DIRF % X", pkts[0][0], lnOPC_LOCO_DIRF)
	}

	// F5 lives in SND.
	rec.reset()
	if err := l.SendFn(MainTrackMode, addr, 5, true); err != nil {
		t.Fatalf("SendFn F5: %v", err)
	}
	pkts = rec.packets()
	if len(pkts) != 1 || pkts[0][0] != lnOPC_LOCO_SND {
		t.Fatalf("F5: frames % X, want single SND % X", pkts, lnOPC_LOCO_SND)
	}
}

// The pacer enforces the minimum inter-frame gap so a burst cannot outrun the
// bus and overflow the transport buffer. Only the lower bound is asserted (a
// sleep never returns early), keeping the test non-flaky.
func TestPacerEnforcesMinGap(t *testing.T) {
	l, _ := newTestLoconet()
	l.minTxGap = 20 * time.Millisecond

	start := time.Now()
	for i := 0; i < 3; i++ {
		if err := l.sendLocked(lnBuildSetSpeed(5, byte(i))); err != nil {
			t.Fatalf("sendLocked: %v", err)
		}
	}
	// 3 frames => at least 2 gaps between them.
	if elapsed := time.Since(start); elapsed < 2*l.minTxGap {
		t.Fatalf("burst took %s, want >= %s (pacer not throttling)", elapsed, 2*l.minTxGap)
	}
}

// MetricsSnapshot must reflect hot-path activity: TX frames/bytes, per-opcode
// counts, coalesced frames, and the active-slot gauge.
func TestMetricsSnapshotCountsTraffic(t *testing.T) {
	l, _ := newTestLoconet()
	const addr LocoAddr = 31
	seedCachedSlot(l, addr, 5)
	l.setDirf(addr, 0x20) // forward, so no DIRF frame on a forward SetSpeed

	if err := l.SetSpeed(addr, 10, true, 128); err != nil {
		t.Fatalf("SetSpeed: %v", err)
	}
	if err := l.SendFn(MainTrackMode, addr, 1, true); err != nil {
		t.Fatalf("SendFn: %v", err)
	}

	s := l.MetricsSnapshot()
	// One SPD (SetSpeed) + one DIRF (SendFn F1) = 2 frames, no DIRF from SetSpeed.
	if s.TxFrames != 2 {
		t.Fatalf("TxFrames = %d, want 2", s.TxFrames)
	}
	if s.TxBytes == 0 {
		t.Fatalf("TxBytes = 0, want > 0")
	}
	if got := s.TxByOpcode[lnOPC_LOCO_SPD]; got != 1 {
		t.Fatalf("TxByOpcode[LOCO_SPD] = %d, want 1", got)
	}
	if got := s.TxByOpcode[lnOPC_LOCO_DIRF]; got != 1 {
		t.Fatalf("TxByOpcode[LOCO_DIRF] = %d, want 1", got)
	}
	if s.SlotsActive != 1 {
		t.Fatalf("SlotsActive = %d, want 1", s.SlotsActive)
	}

	// A superseded speed frame must be counted as coalesced, not transmitted.
	g1 := l.nextSpeedGen(addr)
	_ = l.nextSpeedGen(addr) // supersede g1
	if err := l.writeSpeed(addr, 5, 99, true, g1); !errors.Is(err, ErrSpeedSuperseded) {
		t.Fatalf("writeSpeed(stale): %v, want ErrSpeedSuperseded", err)
	}
	if s2 := l.MetricsSnapshot(); s2.TxCoalesced != 1 {
		t.Fatalf("TxCoalesced = %d, want 1", s2.TxCoalesced)
	}
}

// The RX path (dispatch → countRx) must tally received frames by opcode.
func TestMetricsSnapshotCountsRx(t *testing.T) {
	l, _ := newTestLoconet()
	go l.dispatch()
	t.Cleanup(func() { close(l.stop) })

	// OPC_GPON (83 7C) is a valid 2-byte frame; feed it as a received packet.
	l.rxCh <- lnPacket{lnOPC_GPON, 0x7C}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for RX frame to be counted")
		default:
		}
		if l.MetricsSnapshot().RxByOpcode[lnOPC_GPON] == 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// refreshSlots (keepalive) re-sends the last known speed for every cached slot
// so the master does not purge it.
func TestKeepaliveRefreshesCachedSlots(t *testing.T) {
	l, rec := newTestLoconet()
	const addr LocoAddr = 77
	seedCachedSlot(l, addr, 11)
	l.setDirf(addr, 0x20)
	if err := l.SetSpeed(addr, 25, true, 128); err != nil {
		t.Fatalf("SetSpeed: %v", err)
	}
	rec.reset()

	l.slotMu.Lock()
	l.slotAcquiredAt[addr] = time.Now().Add(-31 * time.Second)
	l.slotMu.Unlock()
	if l.recentlyAcquired(addr) {
		t.Fatal("setup: slot should be aged out before keepalive")
	}

	l.refreshOneKeepaliveSlot()
	pkts := rec.packets()
	if got := countOpcode(pkts, lnOPC_LOCO_SPD); got != 1 {
		t.Fatalf("keepalive: SPD frames = %d, want 1", got)
	}
	if !l.recentlyAcquired(addr) {
		t.Fatal("keepalive should refresh slotAcquiredAt")
	}
}

func TestKeepaliveRoundRobinTouchesEachSlot(t *testing.T) {
	l, rec := newTestLoconet()
	seedCachedSlot(l, 10, 1)
	seedCachedSlot(l, 20, 2)
	l.setSpd(10, 5)
	l.setSpd(20, 6)
	rec.reset()

	l.refreshOneKeepaliveSlot()
	l.refreshOneKeepaliveSlot()
	pkts := rec.packets()
	if got := countOpcode(pkts, lnOPC_LOCO_SPD); got != 2 {
		t.Fatalf("round-robin: SPD frames = %d, want 2", got)
	}
}

func TestMetricsReportsTxQueueGauge(t *testing.T) {
	l, _ := newTestLoconet()
	s := l.MetricsSnapshot()
	if s.TxQueueCap != 64 {
		t.Fatalf("TxQueueCap = %d, want 64", s.TxQueueCap)
	}
}

// TestWriteSpeedSuperseded returns ErrSpeedSuperseded when a newer generation
// overtakes the frame before it is transmitted.
func TestWriteSpeedSuperseded(t *testing.T) {
	l, _ := newTestLoconet()
	seedCachedSlot(l, 10, 1)
	gen := l.nextSpeedGen(10)
	l.nextSpeedGen(10) // bump generation so gen is stale
	err := l.writeSpeed(10, 1, 5, true, gen)
	if !errors.Is(err, ErrSpeedSuperseded) {
		t.Fatalf("writeSpeed err = %v, want ErrSpeedSuperseded", err)
	}
}

func TestEstopEnqueueWhenTxChFull(t *testing.T) {
	l := newLocoNetBase()
	l.t = &recTransport{}
	for i := 0; i < cap(l.txCh); i++ {
		l.txCh <- lnTxJob{}
	}
	go func() {
		job := <-l.txEstopCh
		if job.done != nil {
			job.done <- nil
		}
	}()
	err := l.txEnqueue(lnBuildSetSpeed(1, 1), lnTxPriorityEstop)
	if err != nil {
		t.Fatalf("estop should not block on full txCh: %v", err)
	}
	if len(l.txEstopCh) != 0 {
		t.Fatalf("txEstopCh should be drained, len = %d", len(l.txEstopCh))
	}
}

func TestEstopBypassesFullTxQueue(t *testing.T) {
	l := newLocoNetBase()
	l.minTxGap = 0
	rec := &recTransport{}
	l.t = rec
	go l.txLoop()

	const addr LocoAddr = 42
	seedCachedSlot(l, addr, 7)

	for i := 0; i < cap(l.txCh); i++ {
		select {
		case l.txCh <- lnTxJob{pkt: lnBuildSetSpeed(7, byte(i+2))}:
		default:
		}
	}

	start := time.Now()
	if err := l.EmergencyStop(addr, true); err != nil {
		t.Fatalf("EmergencyStop: %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("EmergencyStop took %v, want fast bypass of saturated txCh", d)
	}

	deadline := time.After(2 * time.Second)
	for rec.count() == 0 {
		select {
		case <-deadline:
			t.Fatal("estop frame not transmitted")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	pkts := rec.packets()
	found := false
	for _, p := range pkts {
		if len(p) >= 3 && p[0] == lnOPC_LOCO_SPD && p[2] == 1 {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no wire-estop SPD frame in %d packets", len(pkts))
	}
}
