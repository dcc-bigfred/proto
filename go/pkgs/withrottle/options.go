package withrottle

import (
	"time"

	"github.com/dcc-bigfred/proto/go/pkgs/drive"
)

type config struct {
	heartbeatSecs float64
	deadman       bool
	readTimeout   time.Duration
	serverName    string
	roster        RosterProvider
	labels        LabelProvider
	trackOn       bool
}

func defaultConfig() config {
	return config{
		heartbeatSecs: heartbeatSecs,
		deadman:       true,
		serverName:    serverName,
		trackOn:       true,
	}
}

// ServerOption configures Listen.
type ServerOption func(*config)

// WithHeartbeatSecs sets the advertised *<secs> interval and deadman window.
func WithHeartbeatSecs(s float64) ServerOption {
	return func(c *config) {
		if s > 0 {
			c.heartbeatSecs = s
		}
	}
}

// WithDeadman enables the server-side heartbeat e-stop. BigFred leaves this
// false and lets remotes.Coordinator own idle/heartbeat policy.
func WithDeadman(enabled bool) ServerOption {
	return func(c *config) { c.deadman = enabled }
}

// WithReadTimeout sets a per-connection read deadline refreshed on every line.
func WithReadTimeout(d time.Duration) ServerOption {
	return func(c *config) { c.readTimeout = d }
}

// WithServerName sets the HT<name> railroad name in the initial burst.
func WithServerName(name string) ServerOption {
	return func(c *config) {
		if name != "" {
			c.serverName = name
		}
	}
}

// WithRosterProvider supplies RL… lines per client (empty → RL0).
func WithRosterProvider(rp RosterProvider) ServerOption {
	return func(c *config) { c.roster = rp }
}

// WithLabelProvider supplies M…L function labels after a default acquire.
func WithLabelProvider(lp LabelProvider) ServerOption {
	return func(c *config) { c.labels = lp }
}

// WithTrackOn sets the initial PPA state advertised in the burst.
func WithTrackOn(on bool) ServerOption {
	return func(c *config) { c.trackOn = on }
}

// RosterProvider returns the roster the server should advertise to a client.
type RosterProvider interface {
	Roster(client drive.ClientID) []RosterEntry
}

// RosterEntry is one locomotive in an RL line.
type RosterEntry struct {
	Name string
	Addr uint16
}

// LabelProvider returns function labels indexed by fn (empty string = gap).
// nil / empty skip the M…L line.
type LabelProvider interface {
	Labels(client drive.ClientID, addr uint16) []string
}
