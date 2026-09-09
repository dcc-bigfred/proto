package withrottle

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/dcc-bigfred/proto/go/pkgs/drive"
)

// FormatRosterLine builds RL<n>]\[name}|{addr}|{S|L sorted by address.
func FormatRosterLine(entries []RosterEntry) string {
	if len(entries) == 0 {
		return rosterEmpty
	}
	sorted := append([]RosterEntry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Addr != sorted[j].Addr {
			return sorted[i].Addr < sorted[j].Addr
		}
		return sorted[i].Name < sorted[j].Name
	})
	var b strings.Builder
	b.WriteString("RL")
	b.WriteString(strconv.Itoa(len(sorted)))
	for _, e := range sorted {
		name := e.Name
		if name == "" {
			name = fmt.Sprintf("Loco %d", e.Addr)
		}
		kind := "S"
		if e.Addr >= 128 {
			kind = "L"
		}
		b.WriteString(entrySep)
		b.WriteString(name)
		b.WriteString(segmentSep)
		b.WriteString(strconv.Itoa(int(e.Addr)))
		b.WriteString(segmentSep)
		b.WriteString(kind)
	}
	return b.String()
}

func (s *Server) rosterLine(client drive.ClientID) string {
	if s.cfg.roster == nil {
		return rosterEmpty
	}
	return FormatRosterLine(s.cfg.roster.Roster(client))
}

// FormatLabelLine builds M{id}L{key}<;>]\[label0]\[… up to the last non-empty label.
func FormatLabelLine(throttleID byte, locoKey string, labels []string) string {
	if len(labels) == 0 {
		return ""
	}
	maxFn := -1
	for i, l := range labels {
		if l != "" {
			maxFn = i
		}
	}
	if maxFn < 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("M")
	b.WriteByte(throttleID)
	b.WriteByte('L')
	b.WriteString(locoKey)
	b.WriteString(propSep)
	for fn := 0; fn <= maxFn; fn++ {
		b.WriteString(entrySep)
		if fn < len(labels) {
			b.WriteString(labels[fn])
		}
	}
	return b.String()
}
