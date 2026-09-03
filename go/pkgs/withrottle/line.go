// Package withrottle implements the WiThrottle TCP line protocol (JMRI,
// DCC-EX, LNWI, RB1110): grammar, a Station client, and a loopback Listen
// server for tests.
package withrottle

import (
	"strconv"
	"strings"
)

const (
	propSep    = "<;>"
	entrySep   = `]\[`
	segmentSep = "}|{"
	maxFn      = 31
)

// MOp is the operation letter in an M command (M«id»«op»…).
type MOp byte

const (
	MOpAdd    MOp = '+'
	MOpRemove MOp = '-'
	MOpSteal  MOp = 'S'
	MOpAction MOp = 'A'
	MOpLabels MOp = 'L'
)

// MCommand is a parsed MultiThrottle line.
type MCommand struct {
	ThrottleID byte
	Op         MOp
	LocoKey    string
	Properties []string
}

// ParseM parses M«id»«op»«locoKey»<;>… lines.
func ParseM(line string) (MCommand, bool) {
	if len(line) < 4 || line[0] != 'M' {
		return MCommand{}, false
	}
	op := line[2]
	if op != '+' && op != '-' && op != 'S' && op != 'A' && op != 'L' {
		return MCommand{}, false
	}
	parts := splitNonEmpty(line[3:], propSep)
	if len(parts) == 0 {
		return MCommand{}, false
	}
	cmd := MCommand{
		ThrottleID: line[1],
		Op:         MOp(op),
		LocoKey:    parts[0],
	}
	if len(parts) > 1 {
		cmd.Properties = parts[1:]
	}
	return cmd, true
}

// ParseLocoKey parses Snnn or Lnnn into a DCC address.
func ParseLocoKey(s string) (addr uint16, isLong bool, ok bool) {
	if len(s) < 2 {
		return 0, false, false
	}
	switch s[0] {
	case 'S', 's':
		n, err := strconv.ParseUint(s[1:], 10, 16)
		if err != nil || n == 0 || n > 127 {
			return 0, false, false
		}
		return uint16(n), false, true
	case 'L', 'l':
		n, err := strconv.ParseUint(s[1:], 10, 16)
		if err != nil || n < 128 || n > 10239 {
			return 0, false, false
		}
		return uint16(n), true, true
	default:
		return 0, false, false
	}
}

// LocoKey formats Snnn / Lnnn for a DCC address.
func LocoKey(addr uint16) string {
	if addr >= 128 {
		return "L" + strconv.Itoa(int(addr))
	}
	return "S" + strconv.Itoa(int(addr))
}

func parseFnDigits(prop string) (fn int, bit bool, ok bool) {
	if len(prop) < 2 {
		return 0, false, false
	}
	if prop[1] != '0' && prop[1] != '1' {
		return 0, false, false
	}
	n, err := strconv.Atoi(prop[2:])
	if err != nil || n < 0 || n > maxFn {
		return 0, false, false
	}
	return n, prop[1] == '1', true
}

// parsePress is F«0|1»«fn» — button press (1) / release (0).
func parsePress(prop string) (fn int, pressed bool, ok bool) {
	if len(prop) == 0 || prop[0] != 'F' {
		return 0, false, false
	}
	return parseFnDigits(prop)
}

// parseForce is f«0|1»«fn» — absolute off/on.
func parseForce(prop string) (fn int, on bool, ok bool) {
	if len(prop) == 0 || prop[0] != 'f' {
		return 0, false, false
	}
	return parseFnDigits(prop)
}

// parseMode is m«0|1»«fn» — latching (0) / momentary (1).
func parseMode(prop string) (fn int, momentary bool, ok bool) {
	if len(prop) == 0 || prop[0] != 'm' {
		return 0, false, false
	}
	return parseFnDigits(prop)
}

func parseSpeedValue(prop string) (speed uint8, ok bool) {
	if len(prop) < 2 || prop[0] != 'V' {
		return 0, false
	}
	n, err := strconv.Atoi(prop[1:])
	if err != nil {
		return 0, false
	}
	if n < 0 {
		return 1, true // negative = e-stop
	}
	if n > 126 {
		n = 126
	}
	return uint8(n), true
}

func parseAcquireAddr(locoKey string, props []string) (uint16, bool) {
	if addr, _, ok := ParseLocoKey(locoKey); ok {
		return addr, true
	}
	for i := len(props) - 1; i >= 0; i-- {
		if addr, _, ok := ParseLocoKey(props[i]); ok {
			return addr, true
		}
	}
	return 0, false
}

func splitNonEmpty(s, sep string) []string {
	if s == "" {
		return nil
	}
	raw := strings.Split(s, sep)
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func buildAcquireReply(throttleID byte, addr uint16, st locoView) []string {
	key := LocoKey(addr)
	id := string(throttleID)
	dir := 0
	if st.Forward {
		dir = 1
	}
	lines := []string{
		"M" + id + "+" + key + propSep,
	}
	for fn := 0; fn <= 28; fn++ {
		bit := 0
		if st.Functions&(1<<uint(fn)) != 0 {
			bit = 1
		}
		lines = append(lines, "M"+id+"A"+key+propSep+"F"+strconv.Itoa(bit)+strconv.Itoa(fn))
	}
	lines = append(lines,
		"M"+id+"A"+key+propSep+"V"+strconv.Itoa(int(st.Speed)),
		"M"+id+"A"+key+propSep+"R"+strconv.Itoa(dir),
		"M"+id+"A"+key+propSep+"s1",
	)
	return lines
}

func buildReleaseLine(throttleID byte, locoKey string) string {
	return "M" + string(throttleID) + "-" + locoKey + propSep
}

func buildNotify(throttleID byte, addr uint16, st locoView) []string {
	key := LocoKey(addr)
	id := string(throttleID)
	dir := 0
	if st.Forward {
		dir = 1
	}
	lines := []string{
		"M" + id + "A" + key + propSep + "V" + strconv.Itoa(int(st.Speed)),
		"M" + id + "A" + key + propSep + "R" + strconv.Itoa(dir),
	}
	for fn := 0; fn <= 28; fn++ {
		if st.Functions&(1<<uint(fn)) == 0 {
			continue
		}
		lines = append(lines, "M"+id+"A"+key+propSep+"F1"+strconv.Itoa(fn))
	}
	return lines
}

type locoView struct {
	Speed     uint8
	Forward   bool
	Functions uint32
}
