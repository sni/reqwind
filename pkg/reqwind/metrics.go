package reqwind

import (
	"path"
	"sort"
	"strings"
	"time"
)

// ClientStats is per-client (hashed ID) request and byte totals for one day.
// Raw holds the client IP unless -hash-clients is set.
type ClientStats struct {
	Requests, Bytes int64
	Raw             string `json:",omitempty"`
}

// Visit is a session interval in UNIX seconds, sorted by Start.
type Visit struct {
	Start, End int64
}

// 30 minutes of inactivity ends a session; a page hit within the gap
// merges into the previous one.
const visitGap = 1800

// isPage reports whether e is a successful GET/POST for a page-like path:
// no file extension, a trailing slash, or a known server-side page extension.
func isPage(e Entry) bool {
	if e.Status < 200 || e.Status >= 300 || (e.Method != "GET" && e.Method != "POST") {
		return false
	}
	p := e.Path
	if p == "" {
		return false
	}
	if strings.HasSuffix(p, "/") {
		return true
	}
	switch strings.ToLower(path.Ext(p)) {
	case "", ".html", ".htm", ".shtml", ".xhtml", ".php", ".asp", ".aspx", ".jsp", ".cgi", ".cfm", ".pl":
		return true
	}
	return false
}

// visitDelta adjusts the page-hit visit count for the day a session starts on.
// Session updates must not recreate days removed by manual retention.
func (s *State) visitDelta(vhost string, start, delta int64) {
	date := time.Unix(start, 0).UTC().Format("2006-01-02")
	if m := s.Months[date[:7]]; m != nil && m.Hosts[vhost][date] != nil {
		s.ensureDay(date, vhost).Visits += delta
	}
}

// trackMetrics records one retained valid entry: file/page counters,
// per-client totals, and the 30-minute session.
func (s *State) trackMetrics(e Entry, d *Day, id string) {
	if e.Status >= 200 && e.Status < 300 && e.Bytes > 0 {
		d.Files++
	}

	if d.Clients == nil {
		d.Clients = map[string]ClientStats{}
	}
	cs := d.Clients[id]
	cs.Requests++
	cs.Bytes += e.Bytes
	if !s.hashClients && cs.Raw == "" {
		cs.Raw = e.Host
	}
	d.Clients[id] = cs
	if !isPage(e) {
		return
	}
	d.Pages++

	if s.Sessions == nil {
		s.Sessions = map[string]map[string][]Visit{}
	}
	if s.Sessions[e.Vhost] == nil {
		s.Sessions[e.Vhost] = map[string][]Visit{}
	}
	t := e.Time.Unix()
	ints := s.Sessions[e.Vhost][id]
	i := sort.Search(len(ints), func(j int) bool { return ints[j].End+visitGap >= t })
	if i < len(ints) && ints[i].Start <= t && t <= ints[i].End {
		return
	}
	merged := Visit{Start: t, End: t}
	j := i
	for j < len(ints) && ints[j].Start <= merged.End+visitGap {
		s.visitDelta(e.Vhost, ints[j].Start, -1)
		merged.Start = min(merged.Start, ints[j].Start)
		merged.End = max(merged.End, ints[j].End)
		j++
	}
	if i == j {
		ints = append(ints, Visit{})
		copy(ints[i+1:], ints[i:])
	} else {
		copy(ints[i+1:], ints[j:])
		ints = ints[:len(ints)-(j-i)+1]
	}
	ints[i] = merged
	s.Sessions[e.Vhost][id] = ints
	s.visitDelta(e.Vhost, merged.Start, 1)
}

// hashClientAddresses also removes raw labels saved by earlier runs.
func (s *State) hashClientAddresses() {
	s.hashClients = true
	for _, month := range s.Months {
		for _, days := range month.Hosts {
			for _, day := range days {
				for id, client := range day.Clients {
					if client.Raw != "" {
						client.Raw = ""
						s.ensureDay(day.Date, day.Vhost).Clients[id] = client
					}
				}
			}
		}
	}
}
