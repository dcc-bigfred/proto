package z21

import "errors"

// ErrInvalidAddress is returned by AddressCVWrites when addr is 0.
var ErrInvalidAddress = errors.New("z21: invalid decoder address")

// CVWrite is one NMRA CV number and value for decoder address programming.
type CVWrite struct {
	CV    uint16
	Value byte
}

// AddressFromCVs decodes a DCC locomotive address from CV1, CV17, CV18, CV29.
// long is true when CV29 bit 5 (0x20) selects the long address (CV17/CV18).
// ok is always true (the CVs always decode to an address).
func AddressFromCVs(cv1, cv17, cv18, cv29 byte) (addr uint16, long bool, ok bool) {
	if cv29&0x20 != 0 {
		return (uint16(cv17&0x3F) << 8) | uint16(cv18), true, true
	}
	return uint16(cv1), false, true
}

// AddressCVWrites returns the CV writes that set the decoder to addr.
// cv29 is the current CV29 value; the long-address bit (0x20) is set or cleared.
func AddressCVWrites(addr uint16, cv29 byte) (writes []CVWrite, long bool, err error) {
	if addr == 0 {
		return nil, false, ErrInvalidAddress
	}
	if addr <= 127 {
		return []CVWrite{
			{CV: 1, Value: byte(addr)},
			{CV: 29, Value: cv29 &^ 0x20},
		}, false, nil
	}
	return []CVWrite{
		{CV: 17, Value: byte(addr>>8) | 0xC0},
		{CV: 18, Value: byte(addr)},
		{CV: 29, Value: cv29 | 0x20},
	}, true, nil
}
