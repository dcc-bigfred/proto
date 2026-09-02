package commandstation

// StubStation is a configurable no-op Station for tests in dependent
// packages. Zero value methods return nil without recording calls.
type StubStation struct {
	SetSpeedFn func(addr LocoAddr, speed uint8, forward bool, speedSteps uint8) error
	SendFnFn   func(mode Mode, addr LocoAddr, num FuncNum, toggle bool) error
}

func (s *StubStation) WriteCV(Mode, LocoCV, ...ctxOptions) error { return nil }
func (s *StubStation) ReadCV(Mode, LocoCV, ...ctxOptions) (int, error) {
	return 0, nil
}
func (s *StubStation) SendFn(mode Mode, addr LocoAddr, num FuncNum, toggle bool) error {
	if s != nil && s.SendFnFn != nil {
		return s.SendFnFn(mode, addr, num, toggle)
	}
	return nil
}
func (s *StubStation) ListFunctions(LocoAddr) ([]int, error) { return nil, nil }
func (s *StubStation) SetSpeed(addr LocoAddr, speed uint8, forward bool, speedSteps uint8) error {
	if s != nil && s.SetSpeedFn != nil {
		return s.SetSpeedFn(addr, speed, forward, speedSteps)
	}
	return nil
}
func (s *StubStation) GetSpeed(LocoAddr) (uint8, bool, error) { return 0, true, nil }
func (s *StubStation) CleanUp() error                          { return nil }
