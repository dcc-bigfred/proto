package z21

import "errors"

// LongMax is the highest DCC long address (CV17/CV18, NMRA).
const LongMax uint16 = 10239

// ErrInvalidAddress is returned by AddressCVWrites when addr is 0 or above LongMax.
var ErrInvalidAddress = errors.New("z21: invalid decoder address")

// RailComPlusCV is ESU CV 28 (RailCom / RailComPlus options).
const RailComPlusCV uint16 = 28

// railComPlusMask is CV 28 bit 7 (auto loco recognition).
const railComPlusMask byte = 1 << 7

// CVWrite is one NMRA CV number and value for decoder address programming.
type CVWrite struct {
	CV    uint16
	Value byte
}

// AddressOption configures AddressCVWrites without changing the required args.
type AddressOption func(*addrOpts)

type addrOpts struct {
	railcom *railcomOpt
}

type railcomOpt struct {
	disable bool
	cv28    *byte
}

// WithRailComPlusDisabled prepends a CV 28 write that sets or clears bit 7.
// disable=true clears the bit; disable=false sets it. cv28 == nil (unread)
// skips the CV 28 write and is not an error. If the bit is already in the
// requested state, CV 28 is not written.
func WithRailComPlusDisabled(disable bool, cv28 *byte) AddressOption {
	return func(o *addrOpts) {
		o.railcom = &railcomOpt{disable: disable, cv28: cv28}
	}
}

// AddressFromCVs decodes a DCC locomotive address from CV1, CV17, CV18, CV29.
// long is true when CV29 bit 5 (0x20) selects the long address (CV17/CV18).
// Short addresses use CV1 bits 0–6. ok is always true (the CVs always decode).
func AddressFromCVs(cv1, cv17, cv18, cv29 byte) (addr uint16, long bool, ok bool) {
	if cv29&0x20 != 0 {
		return (uint16(cv17&0x3F) << 8) | uint16(cv18), true, true
	}
	return uint16(cv1 & 0x7F), false, true
}

// AddressCVWrites returns the CV writes that set the decoder to addr.
// addr must be in 1..=LongMax. cv29 is the current CV29 value; the long-address
// bit (0x20) is set or cleared. Optional WithRailComPlusDisabled prepends CV 28
// before the address CVs.
func AddressCVWrites(addr uint16, cv29 byte, opts ...AddressOption) (writes []CVWrite, long bool, err error) {
	if addr == 0 || addr > LongMax {
		return nil, false, ErrInvalidAddress
	}
	var o addrOpts
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	if extra, ok := railcomWrite(o.railcom); ok {
		writes = append(writes, extra)
	}
	if addr <= 127 {
		return append(writes, CVWrite{CV: 1, Value: byte(addr)}, CVWrite{CV: 29, Value: cv29 &^ 0x20}), false, nil
	}
	return append(writes,
		CVWrite{CV: 17, Value: byte(addr>>8) | 0xC0},
		CVWrite{CV: 18, Value: byte(addr)},
		CVWrite{CV: 29, Value: cv29 | 0x20},
	), true, nil
}

func railcomWrite(opt *railcomOpt) (CVWrite, bool) {
	if opt == nil || opt.cv28 == nil {
		return CVWrite{}, false
	}
	cur := *opt.cv28
	next := cur | railComPlusMask
	if opt.disable {
		next = cur &^ railComPlusMask
	}
	if next == cur {
		return CVWrite{}, false
	}
	return CVWrite{CV: RailComPlusCV, Value: next}, true
}
