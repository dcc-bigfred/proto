package commandstation

import (
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/dcc-bigfred/proto/go/pkgs/drive"
	"github.com/dcc-bigfred/proto/go/pkgs/z21"
)

func TestParseOpenURI(t *testing.T) {
	cases := []struct {
		name    string
		uri     string
		want    openTarget
		wantErr bool
	}{
		{
			name: "z21 recommended scheme",
			uri:  "z21://192.168.0.111:21105",
			want: openTarget{scheme: schemeUDP, host: "192.168.0.111", port: 21105},
		},
		{
			name: "z21 alias udp",
			uri:  "udp://192.168.0.111:21105",
			want: openTarget{scheme: schemeUDP, host: "192.168.0.111", port: 21105},
		},
		{
			name: "z21 default port",
			uri:  "z21://192.168.0.111",
			want: openTarget{scheme: schemeUDP, host: "192.168.0.111", port: 21105},
		},
		{
			name: "withrottle default port",
			uri:  "withrottle://192.168.0.42",
			want: openTarget{scheme: schemeWiThrottle, host: "192.168.0.42", port: 12090},
		},
		{
			name: "withrottle with port",
			uri:  "withrottle://192.168.0.42:12090",
			want: openTarget{scheme: schemeWiThrottle, host: "192.168.0.42", port: 12090},
		},
		{
			name: "serial with baud",
			uri:  "serial:///dev/ttyUSB0:57600",
			want: openTarget{scheme: schemeSerial, device: "/dev/ttyUSB0", baud: 57600},
		},
		{
			name: "serial default baud",
			uri:  "serial:///dev/ttyUSB0",
			want: openTarget{scheme: schemeSerial, device: "/dev/ttyUSB0", baud: 57600},
		},
		{
			name: "serial autodetect",
			uri:  "serial://autodetect:115200",
			want: openTarget{scheme: schemeSerial, device: SerialAutodetectDevice, baud: 115200},
		},
		{
			name: "loconet-tcp recommended scheme",
			uri:  "loconet-tcp://192.168.0.10:1234",
			want: openTarget{scheme: schemeTCP, host: "192.168.0.10", port: 1234},
		},
		{
			name: "loconet binary tcp alias",
			uri:  "tcp://192.168.0.10:1234",
			want: openTarget{scheme: schemeTCP, host: "192.168.0.10", port: 1234},
		},
		{
			name: "loconet binary default port",
			uri:  "loconet-tcp://192.168.0.10",
			want: openTarget{scheme: schemeTCP, host: "192.168.0.10", port: 1234},
		},
		{
			name: "lbserver ascii",
			uri:  "lbserver://192.168.0.10:5550",
			want: openTarget{scheme: schemeLbServer, host: "192.168.0.10", port: 5550},
		},
		{
			name: "lbserver default port",
			uri:  "lbserver://192.168.0.10",
			want: openTarget{scheme: schemeLbServer, host: "192.168.0.10", port: 5550},
		},
		{name: "empty", uri: "", wantErr: true},
		{name: "missing scheme", uri: "192.168.0.111:21105", wantErr: true},
		{name: "unknown scheme", uri: "foo://bar", wantErr: true},
		{name: "invalid port", uri: "z21://192.168.0.111:99999", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseOpenURI(tc.uri)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("got %+v, want error", got)
				}
				if !errors.Is(err, ErrUnsupportedURI) && tc.name != "invalid port" {
					t.Fatalf("err = %v, want ErrUnsupportedURI", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %+v want %+v", got, tc.want)
			}
		})
	}
}

func TestOpenUDPLoopback(t *testing.T) {
	host := &openRecHost{}
	srv, err := z21.Listen("127.0.0.1:0", host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	udp := srv.Addr().(*net.UDPAddr)
	st, err := Open(fmt.Sprintf("z21://127.0.0.1:%d", udp.Port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.CleanUp() })

	if _, ok := st.(*Z21Roco); !ok {
		t.Fatalf("Open z21:// returned %T, want *Z21Roco", st)
	}

	if err := st.SetSpeed(3, 50, true, 128); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(host.speeds) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if len(host.speeds) == 0 || host.speeds[0].Speed != 50 {
		t.Fatalf("SetSpeed host=%+v", host.speeds)
	}
}

type openRecHost struct {
	speeds []drive.LocoState
	locos  map[uint16]drive.LocoState
}

func (h *openRecHost) SetSpeed(_ drive.ClientID, addr uint16, speed uint8, forward bool, steps uint8) error {
	if h.locos == nil {
		h.locos = map[uint16]drive.LocoState{}
	}
	st := h.locos[addr]
	st.Addr, st.Speed, st.Forward, st.Steps = addr, speed, forward, steps
	h.locos[addr] = st
	h.speeds = append(h.speeds, st)
	return nil
}
func (h *openRecHost) SetFunction(drive.ClientID, uint16, uint8, bool) error { return nil }
func (h *openRecHost) LocoState(addr uint16) (drive.LocoState, error) {
	if h.locos != nil {
		if st, ok := h.locos[addr]; ok {
			if st.Steps == 0 {
				st.Steps = 128
			}
			return st, nil
		}
	}
	return drive.LocoState{Addr: addr, Steps: 128}, nil
}
func (h *openRecHost) SetTrackPower(drive.ClientID, bool) error { return nil }
func (h *openRecHost) Release(drive.ClientID, uint16)           {}
