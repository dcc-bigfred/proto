package commandstation

import (
	"encoding/binary"
	"testing"
)

// makeLocoInfo builds a minimal valid LAN_X_LOCO_INFO packet (through
// DB4) for tests. db4 carries F0..F4 (F0 = bit 4).
func makeLocoInfo(addr LocoAddr, db2, db3, db4 byte) []byte {
	pkt := []byte{
		0, 0, // DataLen (filled below)
		0x40, 0x00, // Header 0x0040
		0xEF,                             // X-Header
		byte((uint16(addr) >> 8) & 0x3F), // DB0 Adr_MSB
		byte(uint16(addr) & 0xFF),        // DB1 Adr_LSB
		db2,                              // DB2
		db3,                              // DB3
		db4,                              // DB4
		0x00,                             // XOR (not validated)
	}
	binary.LittleEndian.PutUint16(pkt[0:2], uint16(len(pkt)))
	return pkt
}

func TestParseLocoInfoPacket(t *testing.T) {
	t.Parallel()
	pkt := makeLocoInfo(4, 0x04, 0x80, 0x10) // 128-step, stop, forward, F0 on
	addr, state, speed, forward, ok := parseLocoInfoPacket(pkt)
	if !ok {
		t.Fatalf("parseLocoInfoPacket returned ok=false for valid packet")
	}
	if addr != 4 {
		t.Fatalf("addr = %d, want 4", addr)
	}
	if speed != 0 || !forward {
		t.Fatalf("speed/forward = (%d, %v), want (0, true)", speed, forward)
	}
	if (state.B0_4 & 0x10) == 0 {
		t.Fatalf("expected F0 bit set in DB4")
	}
}

func TestParseLocoInfoPacketRejectsNonInfo(t *testing.T) {
	t.Parallel()
	// A CV result packet (X-Header 0x64), not LOCO_INFO.
	pkt := []byte{0x0A, 0x00, 0x40, 0x00, 0x64, 0x14, 0x00, 0x07, 0x2A, 0x00}
	if _, _, _, _, ok := parseLocoInfoPacket(pkt); ok {
		t.Fatalf("parseLocoInfoPacket accepted a non-LOCO_INFO packet")
	}
}

func TestSplitZ21Datagram(t *testing.T) {
	t.Parallel()
	a := makeLocoInfo(4, 0x04, 0x80, 0x10)
	b := makeLocoInfo(7, 0x04, 0xC0, 0x00)
	datagram := append(append([]byte{}, a...), b...)
	pkts := splitZ21Datagram(datagram)
	if len(pkts) != 2 {
		t.Fatalf("splitZ21Datagram returned %d packets, want 2", len(pkts))
	}
	if a0, _, _, _, ok := parseLocoInfoPacket(pkts[0]); !ok || a0 != 4 {
		t.Fatalf("first packet addr = %d ok=%v, want 4/true", a0, ok)
	}
	if a1, _, _, _, ok := parseLocoInfoPacket(pkts[1]); !ok || a1 != 7 {
		t.Fatalf("second packet addr = %d ok=%v, want 7/true", a1, ok)
	}
}

func TestBuildSetBroadcastFlags(t *testing.T) {
	t.Parallel()
	pkt := buildSetBroadcastFlags(z21BcDrivingSwitching | z21BcAllLocos)
	want := []byte{0x08, 0x00, 0x50, 0x00, 0x01, 0x00, 0x01, 0x00}
	if len(pkt) != len(want) {
		t.Fatalf("len = %d, want %d", len(pkt), len(want))
	}
	for i := range want {
		if pkt[i] != want[i] {
			t.Fatalf("byte %d = %#02x, want %#02x (pkt=% X)", i, pkt[i], want[i], pkt)
		}
	}
}

func TestEncodeLocoDriveDB3(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		speed      uint8
		forward    bool
		speedSteps uint8
		wantDB3    byte
	}{
		{
			name:       "128-step stop forward keeps R bit",
			speed:      0,
			forward:    true,
			speedSteps: 3,
			wantDB3:    0x80,
		},
		{
			name:       "128-step stop reverse",
			speed:      0,
			forward:    false,
			speedSteps: 3,
			wantDB3:    0x00,
		},
		{
			name:       "28-step stop forward keeps R bit",
			speed:      0,
			forward:    true,
			speedSteps: 2,
			wantDB3:    0x80,
		},
		{
			name:       "28-step stop reverse",
			speed:      0,
			forward:    false,
			speedSteps: 2,
			wantDB3:    0x00,
		},
		{
			name:       "128-step e-stop forward",
			speed:      1,
			forward:    true,
			speedSteps: 3,
			wantDB3:    0x81,
		},
		{
			name:       "128-step e-stop reverse",
			speed:      1,
			forward:    false,
			speedSteps: 3,
			wantDB3:    0x01,
		},
		{
			name:       "128-step mid forward",
			speed:      64,
			forward:    true,
			speedSteps: 3,
			wantDB3:    0xC0,
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := encodeLocoDriveDB3(tc.speed, tc.forward, tc.speedSteps)
			if got != tc.wantDB3 {
				t.Fatalf("encodeLocoDriveDB3(%d, %v, %d) = %#02x, want %#02x",
					tc.speed, tc.forward, tc.speedSteps, got, tc.wantDB3)
			}
		})
	}
}

func TestEncodeDecodeLocoDriveRoundtrip(t *testing.T) {
	t.Parallel()
	// 128-step: SET uses S=3, INFO reports KKK=4. Encode with proto 3,
	// decode with db2 KKK=4.
	db2 := byte(0x04)
	for _, tc := range []struct {
		speed   uint8
		forward bool
	}{
		{0, true},
		{0, false},
		{1, true},
		{64, true},
		{127, false},
	} {
		db3 := encodeLocoDriveDB3(tc.speed, tc.forward, 3)
		speed, forward := decodeLocoDriveFromLocoInfo(db2, db3)
		if speed != tc.speed || forward != tc.forward {
			t.Fatalf("roundtrip (%d, %v): encoded %#02x, decoded (%d, %v)",
				tc.speed, tc.forward, db3, speed, forward)
		}
	}
}

func TestDecodeLocoDriveFromLocoInfo(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		db2     byte
		db3     byte
		speed   uint8
		forward bool
	}{
		{
			name:    "28-step Stop1 forward",
			db2:     0x02,
			db3:     0x90, // R=1 (forward) + V5 set (Stop1)
			speed:   0,
			forward: true,
		},
		{
			name:    "28-step stop reverse",
			db2:     0x02,
			db3:     0x00,
			speed:   0,
			forward: false,
		},
		{
			name:    "128-step stop forward",
			db2:     0x04,
			db3:     0x80,
			speed:   0,
			forward: true,
		},
		{
			name:    "128-step stop reverse",
			db2:     0x04,
			db3:     0x00,
			speed:   0,
			forward: false,
		},
		{
			name:    "128-step mid forward",
			db2:     0x04,
			db3:     0xC0,
			speed:   64,
			forward: true,
		},
		{
			name:    "128-step max forward",
			db2:     0x04,
			db3:     0xFF,
			speed:   127,
			forward: true,
		},
		{
			name:    "128-step e-stop",
			db2:     0x04,
			db3:     0x01,
			speed:   1,
			forward: false,
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			speed, forward := decodeLocoDriveFromLocoInfo(tc.db2, tc.db3)
			if speed != tc.speed || forward != tc.forward {
				t.Fatalf("decodeLocoDriveFromLocoInfo(%#02x, %#02x) = (%d, %v), want (%d, %v)",
					tc.db2, tc.db3, speed, forward, tc.speed, tc.forward)
			}
		})
	}
}

func TestBuildProgReadPacketDataLen(t *testing.T) {
	t.Parallel()
	z := &Z21Roco{}
	pkt := z.buildProgReadPacket(CV{Num: 8})
	if got := binary.LittleEndian.Uint16(pkt[0:2]); got != 0x0009 {
		t.Fatalf("DataLen = %#04x, want 0x0009 (LAN_X_CV_READ §6.1)", got)
	}
	if len(pkt) != 9 {
		t.Fatalf("len = %d, want 9", len(pkt))
	}
	want := []byte{0x09, 0x00, 0x40, 0x00, 0x23, 0x11, 0x00, 0x07, 0x35}
	for i := range want {
		if pkt[i] != want[i] {
			t.Fatalf("byte %d = %#02x, want %#02x (pkt=% X)", i, pkt[i], want[i], pkt)
		}
	}
}
