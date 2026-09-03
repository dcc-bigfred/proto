package commandstation

import "testing"

// lastDirf returns the DIRF byte of the last OPC_LOCO_DIRF frame the driver
// transmitted, or 0xFF if none was sent.
func lastDirf(frames [][]byte) byte {
	last := byte(0xFF)
	for _, f := range frames {
		if len(f) >= 3 && f[0] == lnOPC_LOCO_DIRF {
			last = f[2]
		}
	}
	return last
}

// A passive bus observation (echo of our own write, an external throttle, or a
// stale slot broadcast from the command station) must NOT overwrite the DIRF
// function byte we command. Otherwise the next F0..F4 toggle rebuilds the whole
// group from a poisoned cache and a neighbouring function (e.g. F1) flickers.
// This is the regression guard for the "F1 blips on an F2 press" bug.
func TestObserveDoesNotClobberCommandedDirf(t *testing.T) {
	l, srv := newSlotTestLoconet(5, lnSLOT_IN_USE)
	t.Cleanup(func() { close(l.stop) })

	const addr LocoAddr = 98
	l.setSlot(addr, 5) // already owned — revalidate IN_USE without ErrSlotInUse
	if _, err := l.acquireSlot(addr); err != nil {
		t.Fatalf("acquireSlot: %v", err)
	}

	// We command F1 (engine) ON. DIRF starts at 0x20 (forward) from the slot
	// read, so after F1 it is 0x21.
	if err := l.SendFn(MainTrackMode, addr, FuncNum(1), true); err != nil {
		t.Fatalf("SendFn F1: %v", err)
	}
	if got := l.getDirf(addr); got != 0x21 {
		t.Fatalf("after F1 on, cached DIRF = 0x%02X, want 0x21", got)
	}

	// A stale/echoed DIRF arrives on the bus with F1 cleared (0x20). Processed
	// synchronously to make the test deterministic.
	l.observe(lnBuildSetDirF(srv.slot, 0x20))

	if got := l.getDirf(addr); got != 0x21 {
		t.Fatalf("passive observation clobbered commanded DIRF: 0x%02X, want 0x21", got)
	}

	// Pressing F2 must rebuild the group from our cache, preserving F1.
	if err := l.SendFn(MainTrackMode, addr, FuncNum(2), true); err != nil {
		t.Fatalf("SendFn F2: %v", err)
	}
	if got := lastDirf(srv.txFrames()); got != 0x23 {
		t.Fatalf("DIRF sent on F2 press = 0x%02X, want 0x23 (F0..F4: F1+F2 set, forward)", got)
	}
}

// AcquireSlot must skip a redundant command-station round trip when the slot
// was validated very recently (reconnect-storm debounce), but still do the
// round trip the first time.
func TestAcquireSlotDebouncesReconnectStorm(t *testing.T) {
	l, srv := newSlotTestLoconet(7, lnSLOT_IN_USE)
	t.Cleanup(func() { close(l.stop) })

	const addr LocoAddr = 98
	l.setSlot(addr, 7)
	if err := l.AcquireSlot(addr); err != nil {
		t.Fatalf("AcquireSlot #1: %v", err)
	}
	firstCount := countOpcode(srv.txFrames(), lnOPC_LOCO_ADR)
	if firstCount == 0 {
		t.Fatal("first AcquireSlot should query the command station")
	}

	// Immediate re-acquire (e.g. a reconnect 1 s later) must not re-query.
	if err := l.AcquireSlot(addr); err != nil {
		t.Fatalf("AcquireSlot #2: %v", err)
	}
	if got := countOpcode(srv.txFrames(), lnOPC_LOCO_ADR); got != firstCount {
		t.Fatalf("second AcquireSlot issued %d extra LOCO_ADR query(ies); expected debounce", got-firstCount)
	}
}
