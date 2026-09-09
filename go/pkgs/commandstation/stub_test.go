package commandstation

import "testing"

func TestStubEmergencyStopFallsBackToSpeedOne(t *testing.T) {
	var gotSpeed uint8
	var gotFwd bool
	s := &StubStation{
		SetSpeedFn: func(_ LocoAddr, speed uint8, forward bool, _ uint8) error {
			gotSpeed = speed
			gotFwd = forward
			return nil
		},
	}
	if err := s.EmergencyStop(3, false); err != nil {
		t.Fatal(err)
	}
	if gotSpeed != 1 || gotFwd {
		t.Fatalf("EmergencyStop stub = speed %d fwd %v; want 1 false", gotSpeed, gotFwd)
	}
}
