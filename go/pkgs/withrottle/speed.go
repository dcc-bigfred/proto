package withrottle

// DccSpeedFromWire maps WiThrottle V encoding (0–126, 1=e-stop) to DCC speed.
func DccSpeedFromWire(wireSpeed int, speedSteps uint) uint8 {
	if wireSpeed <= 0 {
		return 0
	}
	if wireSpeed == 1 {
		return 0
	}
	if speedSteps == 0 {
		speedSteps = 128
	}
	max := int(speedSteps) - 1
	step := ((wireSpeed-1)*max + 62) / 125
	if step > max {
		step = max
	}
	if step < 0 {
		step = 0
	}
	return uint8(step)
}

// WireSpeedFromDCC maps DCC speed to WiThrottle V encoding (never emits 1).
func WireSpeedFromDCC(speed uint8, speedSteps uint) int {
	if speed == 0 {
		return 0
	}
	if speedSteps == 0 {
		speedSteps = 128
	}
	max := int(speedSteps) - 1
	if max <= 0 {
		return 2
	}
	wire := 1 + (int(speed)*125+max/2)/max
	if wire < 2 {
		wire = 2
	}
	if wire > 126 {
		wire = 126
	}
	return wire
}

// ParseSpeedValue reads a V payload into WiThrottle wire speed 0–126.
// estop is true for V1 and negative V.
func ParseSpeedValue(prop string) (wireSpeed int, estop bool, ok bool) {
	speed, ok := parseSpeedValue(prop)
	if !ok {
		return 0, false, false
	}
	if speed == 1 {
		return 1, true, true
	}
	return int(speed), false, true
}
