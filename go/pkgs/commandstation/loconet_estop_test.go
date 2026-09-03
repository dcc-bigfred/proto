package commandstation

import (
	"testing"
)

// EmergencyStop on a COMMON slot acquires only to send wire speed 1, then
// releases so boot-stop of an idle roster does not leave slots IN_USE.
func TestEmergencyStopReleasesTransientSlot(t *testing.T) {
	l, srv := newSlotTestLoconet(5, lnSLOT_COMMON)
	t.Cleanup(func() { close(l.stop) })

	if err := l.EmergencyStop(42, true); err != nil {
		t.Fatalf("EmergencyStop: %v", err)
	}
	if _, ok := l.getSlot(42); ok {
		t.Fatal("slot should be released after estop on formerly COMMON slot")
	}
	if countOpcode(srv.txFrames(), lnOPC_SLOT_STAT1) != 1 {
		t.Fatalf("want one OPC_SLOT_STAT1 release, got %d", countOpcode(srv.txFrames(), lnOPC_SLOT_STAT1))
	}
}

// EmergencyStop must not release a slot BigFred already held (recent cache).
func TestEmergencyStopKeepsRecentlyHeldSlot(t *testing.T) {
	l, srv := newSlotTestLoconet(7, lnSLOT_COMMON)
	t.Cleanup(func() { close(l.stop) })
	const addr LocoAddr = 42
	seedCachedSlot(l, addr, 7)

	if err := l.EmergencyStop(addr, true); err != nil {
		t.Fatalf("EmergencyStop: %v", err)
	}
	if slot, ok := l.getSlot(addr); !ok || slot != 7 {
		t.Fatalf("slot = %d (ok=%v), want 7 kept", slot, ok)
	}
	if countOpcode(srv.txFrames(), lnOPC_SLOT_STAT1) != 0 {
		t.Fatal("must not release a slot BigFred already held")
	}
}

// EmergencyStop must still stop a loco held IN_USE by a physical FRED, but
// must not adopt ownership, NULL MOVE, or release the slot to COMMON.
func TestEmergencyStopStopsExternalInUseWithoutClaim(t *testing.T) {
	l, srv := newSlotTestLoconet(7, lnSLOT_IN_USE)
	t.Cleanup(func() { close(l.stop) })

	if err := l.EmergencyStop(42, true); err != nil {
		t.Fatalf("EmergencyStop: %v", err)
	}
	if _, ok := l.getSlot(42); ok {
		t.Fatal("must not adopt an external IN_USE slot")
	}
	if countOpcode(srv.txFrames(), lnOPC_MOVE_SLOTS) != 0 {
		t.Fatal("must not NULL MOVE an already IN_USE slot")
	}
	if countOpcode(srv.txFrames(), lnOPC_SLOT_STAT1) != 0 {
		t.Fatal("must not release an already IN_USE slot")
	}
	if countOpcode(srv.txFrames(), lnOPC_LOCO_SPD) != 1 {
		t.Fatal("want one emergency-stop speed frame")
	}
}

// Legacy piggyback: with allocatePhysicalSlots off, EmergencyStop adopts the
// IN_USE mapping and keeps it (does not release FRED's slot).
func TestEmergencyStopKeepsCommandStationInUseSlotWhenPiggyback(t *testing.T) {
	l, srv := newSlotTestLoconet(7, lnSLOT_IN_USE)
	t.Cleanup(func() { close(l.stop) })
	l.SetAllocatePhysicalSlots(false)

	if err := l.EmergencyStop(42, true); err != nil {
		t.Fatalf("EmergencyStop: %v", err)
	}
	if slot, ok := l.getSlot(42); !ok || slot != 7 {
		t.Fatalf("slot = %d (ok=%v), want 7 kept", slot, ok)
	}
	if countOpcode(srv.txFrames(), lnOPC_MOVE_SLOTS) != 0 {
		t.Fatal("must not NULL MOVE an already IN_USE slot")
	}
	if countOpcode(srv.txFrames(), lnOPC_SLOT_STAT1) != 0 {
		t.Fatal("must not release an already IN_USE slot")
	}
}
