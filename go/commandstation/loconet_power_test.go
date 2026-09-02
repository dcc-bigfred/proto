package commandstation

import (
	"bytes"
	"testing"
)

func TestSetTrackPowerEmitsGPONAndGPOFF(t *testing.T) {
	l, srv := newSlotTestLoconet(5, lnSLOT_COMMON)
	t.Cleanup(func() { close(l.stop) })

	if err := l.SetTrackPower(true); err != nil {
		t.Fatalf("SetTrackPower(true): %v", err)
	}
	if err := l.SetTrackPower(false); err != nil {
		t.Fatalf("SetTrackPower(false): %v", err)
	}

	tx := srv.txFrames()
	if countOpcode(tx, lnOPC_GPON) != 1 {
		t.Fatalf("want one OPC_GPON, got % X", tx)
	}
	if countOpcode(tx, lnOPC_GPOFF) != 1 {
		t.Fatalf("want one OPC_GPOFF, got % X", tx)
	}
	if !bytes.Equal(tx[0], []byte{0x83, 0x7C}) {
		t.Fatalf("GPON frame = % X, want 83 7C", tx[0])
	}
	if !bytes.Equal(tx[1], []byte{0x82, 0x7D}) {
		t.Fatalf("GPOFF frame = % X, want 82 7D", tx[1])
	}
}
