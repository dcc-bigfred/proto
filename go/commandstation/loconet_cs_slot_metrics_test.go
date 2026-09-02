package commandstation

import (
	"testing"
)

func TestTrackCsSlotStatusTransitions(t *testing.T) {
	l, _ := newTestLoconet()
	const slot byte = 7

	l.trackCsSlotStatus(slot, lnSLOT_COMMON)
	s := l.MetricsSnapshot()
	if s.CsSlotOccupied != 0 || s.CsSlotReleased != 0 {
		t.Fatalf("COMMON first sight: occupied=%d released=%d, want 0/0", s.CsSlotOccupied, s.CsSlotReleased)
	}

	l.trackCsSlotStatus(slot, lnSLOT_IN_USE)
	s = l.MetricsSnapshot()
	if s.CsSlotOccupied != 1 || s.CsSlotReleased != 0 {
		t.Fatalf("COMMON→IN_USE: occupied=%d released=%d, want 1/0", s.CsSlotOccupied, s.CsSlotReleased)
	}

	// Idempotent: same status again.
	l.trackCsSlotStatus(slot, lnSLOT_IN_USE)
	s = l.MetricsSnapshot()
	if s.CsSlotOccupied != 1 {
		t.Fatalf("duplicate IN_USE: occupied=%d, want 1", s.CsSlotOccupied)
	}

	l.trackCsSlotStatus(slot, lnSLOT_COMMON)
	s = l.MetricsSnapshot()
	if s.CsSlotOccupied != 1 || s.CsSlotReleased != 1 {
		t.Fatalf("IN_USE→COMMON: occupied=%d released=%d, want 1/1", s.CsSlotOccupied, s.CsSlotReleased)
	}
}

func TestTrackCsSlotStatusIgnoresSystemSlots(t *testing.T) {
	l, _ := newTestLoconet()
	l.trackCsSlotStatus(120, lnSLOT_IN_USE)
	s := l.MetricsSnapshot()
	if s.CsSlotOccupied != 0 {
		t.Fatalf("system slot: occupied=%d, want 0", s.CsSlotOccupied)
	}
}

func TestObserveSlotStat1CountsCsRelease(t *testing.T) {
	l, _ := newTestLoconet()
	l.trackCsSlotStatus(3, lnSLOT_IN_USE)

	pkt := lnBuildSlotStat1(3, lnSLOT_COMMON)
	l.observe(pkt)

	s := l.MetricsSnapshot()
	if s.CsSlotReleased != 1 {
		t.Fatalf("CsSlotReleased = %d, want 1", s.CsSlotReleased)
	}
}
