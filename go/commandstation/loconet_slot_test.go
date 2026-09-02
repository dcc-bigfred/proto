package commandstation

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// slotServerTransport is a fake command station for slot tests. It records
// every TX frame and answers OPC_LOCO_ADR / OPC_MOVE_SLOTS with a slot read,
// so AcquireSlot can complete its request/response handshake on a loopback.
type slotServerTransport struct {
	rxCh chan<- lnPacket

	mu    sync.Mutex
	tx    [][]byte
	slot  byte // slot number the station reports for the queried address
	stat1 byte // STAT1 returned for OPC_LOCO_ADR (e.g. COMMON or IN_USE)
	speed byte // SPD reported in the slot read
	dirf  byte // DIRF reported in the slot read (0x20 = forward)
	snd   byte // SND reported in the slot read
}

func (s *slotServerTransport) WritePacket(pkt []byte) error {
	s.mu.Lock()
	s.tx = append(s.tx, append([]byte(nil), pkt...))
	slot, stat1, speed, dirf, snd := s.slot, s.stat1, s.speed, s.dirf, s.snd
	s.mu.Unlock()

	if len(pkt) == 0 {
		return nil
	}
	switch pkt[0] {
	case lnOPC_LOCO_ADR:
		addr := LocoAddr(pkt[2]&0x7F) | (LocoAddr(pkt[1]&0x7F) << 7)
		s.rxCh <- buildSlotRead(slot, stat1, addr, speed, dirf, snd)
	case lnOPC_SLOT_STAT1:
		if len(pkt) >= 3 {
			s.mu.Lock()
			s.stat1 = pkt[2]
			s.mu.Unlock()
		}
	case lnOPC_MOVE_SLOTS:
		// NULL MOVE (src==dst): confirm by echoing the slot as IN_USE.
		if pkt[1] == pkt[2] {
			s.mu.Lock()
			s.stat1 = lnSLOT_IN_USE
			s.mu.Unlock()
			s.rxCh <- buildSlotRead(pkt[1], lnSLOT_IN_USE, 0, 0, 0x20, 0)
		}
	case lnOPC_RQ_SL_DATA:
		if len(pkt) >= 2 {
			s.mu.Lock()
			cur := s.stat1
			s.mu.Unlock()
			s.rxCh <- buildSlotRead(pkt[1], cur, 0, speed, dirf, snd)
		}
	}
	return nil
}

func (s *slotServerTransport) Close() error { return nil }

func (s *slotServerTransport) txFrames() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]byte, len(s.tx))
	copy(out, s.tx)
	return out
}

func buildSlotRead(slot, stat1 byte, addr LocoAddr, speed, dirf, snd byte) lnPacket {
	msg := []byte{
		lnOPC_SL_RD_DATA, 0x0E,
		slot,
		stat1,
		byte(addr & 0x7F), // adr lo
		speed,             // speed
		dirf,              // dirf
		0x00,              // trk
		0x00,              // stat2
		byte((addr >> 7) & 0x7F), // adr hi
		snd,                      // snd
		0x00,                     // id1
		0x00,                     // id2
	}
	return lnPacket(lnAppendChecksum(msg))
}

func newSlotTestLoconet(slot, stat1 byte) (*LocoNet, *slotServerTransport) {
	l := newLocoNetBase()
	l.minTxGap = 0
	srv := &slotServerTransport{rxCh: l.rxCh, slot: slot, stat1: stat1, dirf: 0x20}
	l.t = srv
	go l.txLoop()
	go l.dispatch()
	return l, srv
}

// AcquireSlot must reclaim a slot the command station purged to COMMON while
// the loco was idle: it queries fresh, caches the slot, and asserts IN_USE via
// a NULL MOVE.
func TestAcquireSlotReclaimsCommonSlot(t *testing.T) {
	l, srv := newSlotTestLoconet(5, lnSLOT_COMMON)
	t.Cleanup(func() { close(l.stop) })
	const addr LocoAddr = 31

	if err := l.AcquireSlot(addr); err != nil {
		t.Fatalf("AcquireSlot: %v", err)
	}

	if slot, ok := l.getSlot(addr); !ok || slot != 5 {
		t.Fatalf("cached slot = %d (ok=%v), want 5", slot, ok)
	}
	tx := srv.txFrames()
	if countOpcode(tx, lnOPC_LOCO_ADR) != 1 {
		t.Fatalf("expected one OPC_LOCO_ADR, got % X", tx)
	}
	if countOpcode(tx, lnOPC_MOVE_SLOTS) != 1 {
		t.Fatalf("expected one NULL MOVE for a COMMON slot, got % X", tx)
	}
}

// On subscription, AcquireSlot must refresh BigFred's cache (speed, direction
// and the F0..F8 groups) straight from the bus slot reply, so a returning
// client and the keepalive/fast paths see the command station's current view
// rather than a stale value.
func TestAcquireSlotRefreshesCacheFromBus(t *testing.T) {
	l := newLocoNetBase()
	l.minTxGap = 0
	// dirf 0x20 (forward) + F1 (bit 0) on; snd has F5 (bit 0) on.
	// Slot already IN_USE and owned by BigFred (seeded cache).
	srv := &slotServerTransport{
		rxCh:  l.rxCh,
		slot:  6,
		stat1: lnSLOT_IN_USE,
		speed: 73,
		dirf:  0x20 | 0x01,
		snd:   0x01,
	}
	l.t = srv
	go l.txLoop()
	go l.dispatch()
	t.Cleanup(func() { close(l.stop) })

	const addr LocoAddr = 31
	l.setSlot(addr, 6)

	if err := l.AcquireSlot(addr); err != nil {
		t.Fatalf("AcquireSlot: %v", err)
	}

	if spd, ok := l.getSpd(addr); !ok || spd != 73 {
		t.Fatalf("cached speed = %d (ok=%v), want 73 from bus slot data", spd, ok)
	}
	if dirf := l.getDirf(addr); dirf != (0x20 | 0x01) {
		t.Fatalf("cached dirf = %#x, want %#x", dirf, 0x20|0x01)
	}
	if snd := l.getSnd(addr); snd != 0x01 {
		t.Fatalf("cached snd = %#x, want 0x01", snd)
	}
}

// COMMON with 128-step decoder bits (STAT1 0x13) must acquire via NULL MOVE,
// not return ErrSlotInUse — regression for the wrong D1/D0 status mask.
func TestAcquireSlotTreatsCommon128StepAsAvailable(t *testing.T) {
	l, srv := newSlotTestLoconet(5, lnSLOT_COMMON|0x03)
	t.Cleanup(func() { close(l.stop) })

	if err := l.AcquireSlot(31); err != nil {
		t.Fatalf("AcquireSlot: %v (COMMON|128-step must not be ErrSlotInUse)", err)
	}
	if countOpcode(srv.txFrames(), lnOPC_MOVE_SLOTS) != 1 {
		t.Fatalf("expected NULL MOVE for COMMON|128-step, got % X", srv.txFrames())
	}
}

// With allocatePhysicalSlots enabled (default), an already IN_USE slot not
// owned by BigFred must be refused — no NULL MOVE and no ownership cache.
func TestAcquireSlotRejectsExternalInUse(t *testing.T) {
	l, srv := newSlotTestLoconet(7, lnSLOT_IN_USE)
	t.Cleanup(func() { close(l.stop) })

	err := l.AcquireSlot(42)
	if !errors.Is(err, ErrSlotInUse) {
		t.Fatalf("AcquireSlot: %v, want ErrSlotInUse", err)
	}
	if _, ok := l.getSlot(42); ok {
		t.Fatal("must not adopt an external IN_USE slot into ownership cache")
	}
	if countOpcode(srv.txFrames(), lnOPC_MOVE_SLOTS) != 0 {
		t.Fatalf("must not NULL MOVE an already IN_USE slot")
	}
}

// Revalidating a slot BigFred already owns succeeds without NULL MOVE even
// when the command station reports IN_USE.
func TestAcquireSlotAllowsOwnedInUse(t *testing.T) {
	l, srv := newSlotTestLoconet(7, lnSLOT_IN_USE)
	t.Cleanup(func() { close(l.stop) })
	const addr LocoAddr = 42
	l.setSlot(addr, 7)

	if err := l.AcquireSlot(addr); err != nil {
		t.Fatalf("AcquireSlot: %v", err)
	}
	if countOpcode(srv.txFrames(), lnOPC_MOVE_SLOTS) != 0 {
		t.Fatalf("must not NULL MOVE an already IN_USE slot we own")
	}
}

// Legacy piggyback: with allocatePhysicalSlots disabled, BigFred may drive a
// slot already IN_USE by a physical FRED without stealing (no NULL MOVE).
func TestAcquireSlotPiggybacksWhenAllocatePhysicalOff(t *testing.T) {
	l, srv := newSlotTestLoconet(7, lnSLOT_IN_USE)
	t.Cleanup(func() { close(l.stop) })
	l.SetAllocatePhysicalSlots(false)

	if err := l.AcquireSlot(42); err != nil {
		t.Fatalf("AcquireSlot: %v", err)
	}
	if slot, ok := l.getSlot(42); !ok || slot != 7 {
		t.Fatalf("cached slot = %d (ok=%v), want 7", slot, ok)
	}
	if countOpcode(srv.txFrames(), lnOPC_MOVE_SLOTS) != 0 {
		t.Fatalf("must not NULL MOVE an already IN_USE slot")
	}
}

// When the command station reassigns the loco to a different slot while idle,
// AcquireSlot must re-map the cache (forward and reverse) so later speed/function
// frames target the new slot, not the stale one.
func TestAcquireSlotRemapsReassignedSlot(t *testing.T) {
	l, _ := newSlotTestLoconet(9, lnSLOT_COMMON)
	t.Cleanup(func() { close(l.stop) })
	const addr LocoAddr = 55

	// Seed a stale mapping: addr was previously on slot 3.
	l.setSlot(addr, 3)
	if got, _ := l.slotToAddr(3); got != addr {
		t.Fatalf("precondition: slot 3 should map to addr %d", addr)
	}

	if err := l.AcquireSlot(addr); err != nil {
		t.Fatalf("AcquireSlot: %v", err)
	}

	if slot, ok := l.getSlot(addr); !ok || slot != 9 {
		t.Fatalf("cached slot = %d (ok=%v), want 9 after reassignment", slot, ok)
	}
	if _, ok := l.slotToAddr(3); ok {
		t.Fatal("stale reverse mapping for old slot 3 was not cleared")
	}
	if got, ok := l.slotToAddr(9); !ok || got != addr {
		t.Fatalf("reverse mapping for new slot 9 = %d (ok=%v), want %d", got, ok, addr)
	}
}

// AcquireSlot rejects address 0 (the dispatch/system slot sentinel).
func TestAcquireSlotRejectsZeroAddr(t *testing.T) {
	l, _ := newSlotTestLoconet(1, lnSLOT_COMMON)
	t.Cleanup(func() { close(l.stop) })
	if err := l.AcquireSlot(0); err == nil {
		t.Fatal("expected error for addr 0")
	}
}

// Guard against a regression where AcquireSlot blocks forever if the station
// never answers: it must honour slotTimeout and return an error.
func TestAcquireSlotTimesOut(t *testing.T) {
	l := newLocoNetBase()
	l.minTxGap = 0
	l.slotTimeout = 50 * time.Millisecond
	l.t = &recTransport{} // records writes, never replies
	go l.txLoop()
	go l.dispatch()
	t.Cleanup(func() { close(l.stop) })

	start := time.Now()
	if err := l.AcquireSlot(31); err == nil {
		t.Fatal("expected timeout error when station is silent")
	}
	// Two attempts (initial + lnSlotAcquireRetries) bounded by slotTimeout each.
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("AcquireSlot took %s, expected to fail fast", elapsed)
	}
}

// TestSlotBreakerTripsAfterRepeatedFailures verifies the circuit breaker opens
// after consecutive slot-acquire timeouts and blocks further attempts.
func TestSlotBreakerTripsAfterRepeatedFailures(t *testing.T) {
	l := newLocoNetBase()
	l.minTxGap = 0
	l.slotTimeout = 20 * time.Millisecond
	l.t = &recTransport{}
	go l.txLoop()
	go l.dispatch()
	t.Cleanup(func() { close(l.stop) })

	if err := l.AcquireSlot(31); err == nil {
		t.Fatal("expected acquire failure")
	}
	start := time.Now()
	err := l.AcquireSlot(32)
	if err == nil {
		t.Fatal("expected breaker to block second acquire")
	}
	if !errors.Is(err, ErrSlotBusUnavailable) {
		t.Fatalf("breaker error = %v, want ErrSlotBusUnavailable", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("breaker should fail fast, took %s", elapsed)
	}
}

// slotServerWithForeign injects an OPC_SL_RD_DATA for another loco before
// answering the requested address, mimicking traffic from an external throttle
// on a shared bus during slot acquisition.
type slotServerWithForeign struct {
	slotServerTransport
	foreignAddr LocoAddr
	foreignSlot byte
}

func (s *slotServerWithForeign) WritePacket(pkt []byte) error {
	if len(pkt) > 0 && pkt[0] == lnOPC_LOCO_ADR {
		addr := LocoAddr(pkt[2]&0x7F) | (LocoAddr(pkt[1]&0x7F) << 7)
		s.rxCh <- buildSlotRead(s.foreignSlot, lnSLOT_COMMON, s.foreignAddr, 40, 0x20|0x08, 0x04)
		s.rxCh <- buildSlotRead(s.slot, s.stat1, addr, s.speed, s.dirf, s.snd)
		s.mu.Lock()
		s.tx = append(s.tx, append([]byte(nil), pkt...))
		s.mu.Unlock()
		return nil
	}
	return s.slotServerTransport.WritePacket(pkt)
}

// AcquireSlot must ignore foreign E7 traffic during its wait window so a
// released slot is not re-adopted and foreign DIRF/SND never seed our cache.
func TestAcquireSlotIgnoresForeignSlotRead(t *testing.T) {
	l := newLocoNetBase()
	l.minTxGap = 0
	const foreign LocoAddr = 99
	const want LocoAddr = 31
	srv := &slotServerWithForeign{
		slotServerTransport: slotServerTransport{rxCh: l.rxCh, slot: 5, stat1: lnSLOT_COMMON, dirf: 0x20},
		foreignAddr:         foreign,
		foreignSlot:         20,
	}
	l.t = srv
	go l.txLoop()
	go l.dispatch()
	t.Cleanup(func() { close(l.stop) })

	// BigFred previously owned foreign and released it.
	l.setSlot(foreign, 20)
	l.clearSlot(foreign, 20)
	if _, ok := l.getSlot(foreign); ok {
		t.Fatal("precondition: foreign slot must not be cached")
	}

	if err := l.AcquireSlot(want); err != nil {
		t.Fatalf("AcquireSlot: %v", err)
	}
	if _, ok := l.getSlot(foreign); ok {
		t.Fatal("foreign E7 during acquire must not re-adopt released slot")
	}
	if dirf := l.getDirf(foreign); dirf != 0 {
		t.Fatalf("foreign dirf cache = %#x, want 0", dirf)
	}
	if slot, ok := l.getSlot(want); !ok || slot != 5 {
		t.Fatalf("wanted slot for %d = %d (ok=%v), want 5", want, slot, ok)
	}
}

// ReleaseSlot must write COMMON while preserving decoder-type bits from the
// cached STAT1 (PE 1.0 §9.1). Bare 0x10 would force 28-step mode on the master.
func TestReleaseSlotPreservesDecoderModeBits(t *testing.T) {
	const dec128 = byte(0x03)
	l, srv := newSlotTestLoconet(5, lnSLOT_COMMON|dec128)
	t.Cleanup(func() { close(l.stop) })
	const addr LocoAddr = 31

	if err := l.AcquireSlot(addr); err != nil {
		t.Fatalf("AcquireSlot: %v", err)
	}
	if got := l.getStat1(addr); got != (lnSLOT_COMMON | dec128) {
		t.Fatalf("cached STAT1 = %#x, want %#x", got, lnSLOT_COMMON|dec128)
	}

	if err := l.ReleaseSlot(addr); err != nil {
		t.Fatalf("ReleaseSlot: %v", err)
	}
	wantStat1 := lnSLOT_COMMON | dec128
	got := lastStat1(srv.txFrames())
	if got != wantStat1 {
		t.Fatalf("OPC_SLOT_STAT1 byte = %#x, want %#x (COMMON|128-step)", got, wantStat1)
	}
	if _, ok := l.getSlot(addr); ok {
		t.Fatal("slot cache should be cleared after release")
	}
}

func lastStat1(frames [][]byte) byte {
	last := byte(0xFF)
	for _, f := range frames {
		if len(f) >= 3 && f[0] == lnOPC_SLOT_STAT1 {
			last = f[2]
		}
	}
	return last
}

// StealSlot must stop, release foreign IN_USE to COMMON, then NULL MOVE and
// adopt ownership — the explicit takeover path AcquireSlot refuses.
func TestStealSlotClaimsExternalInUse(t *testing.T) {
	const dec128 = byte(0x03)
	l, srv := newSlotTestLoconet(7, lnSLOT_IN_USE|dec128)
	t.Cleanup(func() { close(l.stop) })
	const addr LocoAddr = 42

	if err := l.StealSlot(addr); err != nil {
		t.Fatalf("StealSlot: %v", err)
	}
	if slot, ok := l.getSlot(addr); !ok || slot != 7 {
		t.Fatalf("cached slot = %d (ok=%v), want 7", slot, ok)
	}
	tx := srv.txFrames()
	if countOpcode(tx, lnOPC_LOCO_SPD) < 1 {
		t.Fatalf("expected estop SPD before release, got % X", tx)
	}
	if got := lastStat1(tx); got != (lnSLOT_COMMON | dec128) {
		t.Fatalf("STAT1 = %#x, want COMMON|128-step %#x", got, lnSLOT_COMMON|dec128)
	}
	if countOpcode(tx, lnOPC_MOVE_SLOTS) != 1 {
		t.Fatalf("expected one NULL MOVE after steal, got % X", tx)
	}
}

// AcquireSlot must still refuse foreign IN_USE after StealSlot exists.
func TestAcquireSlotStillRejectsExternalInUse(t *testing.T) {
	l, srv := newSlotTestLoconet(7, lnSLOT_IN_USE)
	t.Cleanup(func() { close(l.stop) })

	if err := l.AcquireSlot(42); !errors.Is(err, ErrSlotInUse) {
		t.Fatalf("AcquireSlot: %v, want ErrSlotInUse", err)
	}
	if countOpcode(srv.txFrames(), lnOPC_MOVE_SLOTS) != 0 {
		t.Fatal("AcquireSlot must not NULL MOVE foreign IN_USE")
	}
}

// StealSlot on a COMMON slot behaves like a normal acquire (NULL MOVE only).
func TestStealSlotPromotesCommon(t *testing.T) {
	l, srv := newSlotTestLoconet(5, lnSLOT_COMMON|0x03)
	t.Cleanup(func() { close(l.stop) })

	if err := l.StealSlot(31); err != nil {
		t.Fatalf("StealSlot: %v", err)
	}
	if countOpcode(srv.txFrames(), lnOPC_SLOT_STAT1) != 0 {
		t.Fatalf("COMMON steal must not write STAT1, got % X", srv.txFrames())
	}
	if countOpcode(srv.txFrames(), lnOPC_MOVE_SLOTS) != 1 {
		t.Fatalf("expected NULL MOVE, got % X", srv.txFrames())
	}
}
