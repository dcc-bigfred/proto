package commandstation

import "testing"

func TestXorSum(t *testing.T) {
	cases := []struct {
		input    []byte
		expected byte
	}{
		{[]byte{}, 0},
		{[]byte{0x00}, 0x00},
		{[]byte{0x01}, 0x01},
		{[]byte{0x01, 0x02}, 0x03},
		{[]byte{0xFF, 0x01}, 0xFE},
		{[]byte{0xAA, 0x55}, 0xFF},
		{[]byte{0x10, 0x20, 0x30}, 0x00},
		{[]byte{0x01, 0x01, 0x01}, 0x01},
	}

	for _, c := range cases {
		got := xorSum(c.input)
		if got != c.expected {
			t.Errorf("xorSum(%v) = %02X; want %02X", c.input, got, c.expected)
		}
	}
}
