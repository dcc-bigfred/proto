package commandstation

import (
	"errors"

	"github.com/dcc-bigfred/proto/go/pkgs/withrottle"
)

var (
	_ Station                  = (*WiThrottle)(nil)
	_ EmergencyStopper         = (*WiThrottle)(nil)
	_ TrackPowerController     = (*WiThrottle)(nil)
	_ withrottle.MetricsSource = (*WiThrottle)(nil)
)

// WiThrottle is a Station over the WiThrottle TCP protocol (JMRI, DCC-EX, LNWI, RB1110).
type WiThrottle struct {
	c *withrottle.Client
}

// NewWiThrottle dials host:port as a WiThrottle throttle. port 0 uses 12090.
func NewWiThrottle(host string, port uint16, opts ...withrottle.Option) (*WiThrottle, error) {
	if port == 0 {
		port = withrottle.DefaultPort
	}
	c, err := withrottle.NewClient(host, port, opts...)
	if err != nil {
		return nil, err
	}
	return &WiThrottle{c: c}, nil
}

func (w *WiThrottle) mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, withrottle.ErrStealRefused) {
		return ErrSlotInUse
	}
	if errors.Is(err, withrottle.ErrUnsupported) {
		return ErrUnsupported
	}
	return err
}

func (w *WiThrottle) SetSpeed(addr LocoAddr, speed uint8, forward bool, speedSteps uint8) error {
	return w.mapErr(w.c.SetSpeed(uint16(addr), speed, forward, speedSteps))
}

func (w *WiThrottle) GetSpeed(addr LocoAddr) (uint8, bool, error) {
	speed, forward, err := w.c.GetSpeed(uint16(addr))
	return speed, forward, w.mapErr(err)
}

func (w *WiThrottle) EmergencyStop(addr LocoAddr, forward bool) error {
	return w.mapErr(w.c.EmergencyStop(uint16(addr), forward))
}

func (w *WiThrottle) SendFn(_ Mode, addr LocoAddr, num FuncNum, toggle bool) error {
	return w.mapErr(w.c.SendFn(uint16(addr), uint8(num), toggle))
}

func (w *WiThrottle) ListFunctions(addr LocoAddr) ([]int, error) {
	fns, err := w.c.ListFunctions(uint16(addr))
	return fns, w.mapErr(err)
}

func (w *WiThrottle) ReadCV(Mode, LocoCV, ...Option) (int, error) {
	return 0, ErrUnsupported
}

func (w *WiThrottle) WriteCV(Mode, LocoCV, ...Option) error {
	return ErrUnsupported
}

func (w *WiThrottle) SetTrackPower(on bool) error {
	return w.c.SetTrackPower(on)
}

func (w *WiThrottle) CleanUp() error {
	return w.c.CleanUp()
}

// Metrics implements withrottle.MetricsSource for telemetry.RegisterWithrottle.
func (w *WiThrottle) Metrics() withrottle.Snapshot {
	return w.c.Metrics()
}
