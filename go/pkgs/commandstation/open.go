package commandstation

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/dcc-bigfred/proto/go/pkgs/withrottle"
	"github.com/dcc-bigfred/proto/go/pkgs/z21"
)

// ErrUnsupportedURI is returned by Open when the connection string is empty
// or uses a scheme this package does not understand.
var ErrUnsupportedURI = errors.New("commandstation: unsupported connection URI")

const (
	schemeUDP        = "udp"
	schemeZ21        = "z21"
	schemeWiThrottle = "withrottle"
	schemeSerial     = "serial"
	schemeTCP        = "tcp"
	schemeLocoNetTCP = "loconet-tcp"
	schemeLbServer   = "lbserver"

	defaultSerialBaud = 57600
	defaultLocoNetTCP = 1234
	defaultLbServer   = 5550
)

// Open dials the command station described by uri. The scheme selects the
// driver; this is the recommended way to connect:
//
//	z21://host:port          Z21 LAN (default port 21105); udp:// also accepted
//	withrottle://host:port   WiThrottle TCP (default port 12090)
//	serial://device:baud     LocoNet serial (default baud 57600)
//	loconet-tcp://host:port  raw LocoNet TCP (default port 1234); tcp:// also accepted
//	lbserver://host:port     LoconetOverTcp ASCII (default port 5550)
//
// opts are applied only to withrottle:// (passed to NewWiThrottle).
// The caller must Close the station with CleanUp.
func Open(uri string, opts ...withrottle.Option) (Station, error) {
	t, err := parseOpenURI(uri)
	if err != nil {
		return nil, err
	}
	switch t.scheme {
	case schemeUDP:
		return NewZ21Roco(t.host, t.port)
	case schemeWiThrottle:
		return NewWiThrottle(t.host, t.port, opts...)
	case schemeSerial:
		return NewLocoNetSerial(t.device, t.baud)
	case schemeTCP:
		return NewLocoNetTCPBinary(t.host, t.port)
	case schemeLbServer:
		return NewLocoNetTCP(t.host, t.port)
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedURI, t.scheme)
	}
}

type openTarget struct {
	scheme string
	host   string
	port   uint16
	device string
	baud   int
}

func parseOpenURI(uri string) (openTarget, error) {
	s := strings.TrimSpace(uri)
	if s == "" {
		return openTarget{}, fmt.Errorf("%w: empty", ErrUnsupportedURI)
	}
	scheme, rest, ok := strings.Cut(s, "://")
	if !ok {
		return openTarget{}, fmt.Errorf("%w: missing scheme in %q", ErrUnsupportedURI, uri)
	}
	scheme = strings.ToLower(scheme)
	t := openTarget{scheme: scheme}
	var err error
	switch scheme {
	case schemeUDP, schemeZ21:
		t.scheme = schemeUDP
		t.host, t.port, err = parseHostPort(rest, z21.DefaultPort)
	case schemeWiThrottle:
		t.host, t.port, err = parseHostPort(rest, withrottle.DefaultPort)
	case schemeTCP, schemeLocoNetTCP:
		t.scheme = schemeTCP
		t.host, t.port, err = parseHostPort(rest, defaultLocoNetTCP)
	case schemeLbServer:
		t.host, t.port, err = parseHostPort(rest, defaultLbServer)
	case schemeSerial:
		t.device, t.baud, err = parseSerial(rest)
	default:
		return openTarget{}, fmt.Errorf("%w: %s", ErrUnsupportedURI, scheme)
	}
	if err != nil {
		return openTarget{}, fmt.Errorf("%s uri %q: %w", scheme, uri, err)
	}
	return t, nil
}

func parseHostPort(s string, defaultPort uint16) (string, uint16, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", 0, fmt.Errorf("empty host")
	}
	host, portStr, err := net.SplitHostPort(s)
	if err != nil {
		return s, defaultPort, nil
	}
	p, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return "", 0, fmt.Errorf("invalid port: %w", err)
	}
	return host, uint16(p), nil
}

func parseSerial(s string) (string, int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", 0, fmt.Errorf("empty device")
	}
	idx := strings.LastIndex(s, ":")
	if idx < 0 {
		return s, defaultSerialBaud, nil
	}
	device := s[:idx]
	if device == "" {
		return "", 0, fmt.Errorf("empty device")
	}
	baud, err := strconv.Atoi(s[idx+1:])
	if err != nil {
		return s, defaultSerialBaud, nil
	}
	return device, baud, nil
}
