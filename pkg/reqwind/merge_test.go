package reqwind

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	saltOne = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	saltTwo = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
)

func mergeDay(date, host string, req int64, visitor string, country int64) *Day {
	return &Day{
		Date: date, Vhost: host, Requests: req, Bytes: req * 100,
		Status:    map[string]int64{"200": req},
		Paths:     map[string]int64{"/": req},
		PathBytes: map[string]int64{"/": req * 100},
		Referrers: map[string]int64{},
		Agents:    map[string]int64{},
		Terms:     map[string]int64{},
		Visitors:  map[string]bool{visitor: true},
		Clients:   map[string]ClientStats{visitor: {Requests: req, Bytes: req * 100}},
		Countries: map[string]int64{"US": country},
	}
}

func writeMergeState(t *testing.T, path string, salt string, days map[string]*Day) {
	t.Helper()
	s := &State{Version: stateVersion, Salt: salt, Files: map[string]checkpoint{}, Months: map[string]*Month{}}
	for _, d := range days {
		m := s.Months[d.Date[:7]]
		if m == nil {
			m = &Month{Hosts: map[string]map[string]*Day{}}
			s.Months[d.Date[:7]] = m
		}
		if m.Hosts[d.Vhost] == nil {
			m.Hosts[d.Vhost] = map[string]*Day{}
		}
		m.Hosts[d.Vhost][d.Date] = d
	}
	// Months is kept out of the meta file; saveState writes it plus one
	// bucket file per month beside it.
	if err := s.saveState(path); err != nil {
		t.Fatal(err)
	}
}

func mergeTotals(days []Day) (requests, byt int64, visitors, clients int, countries int64) {
	vis := map[string]bool{}
	cli := map[string]int64{}
	for _, d := range days {
		requests += d.Requests
		byt += d.Bytes
		for id := range d.Visitors {
			vis[id] = true
		}
		for id := range d.Clients {
			cli[id]++
		}
		for _, n := range d.Countries {
			countries += n
		}
	}
	return requests, byt, len(vis), len(cli), countries
}

// mergeStates pins time.Local to UTC so tests that write date-stamped state
// files are deterministic on any host.
func mergeStates(t *testing.T, f func()) {
	t.Helper()
	previous := time.Local
	time.Local = time.UTC
	defer func() { time.Local = previous }()
	f()
}

func TestMergeNamespacingAndSums(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.json")
	b := filepath.Join(dir, "b.json")
	c := filepath.Join(dir, "c.json")
	// All states hold the same visitor id for the same client address;
	// only same-salt states may see each other.
	writeMergeState(t, a, saltOne, map[string]*Day{
		"2026-11-10": mergeDay("2026-11-10", "example.org", 3, "v1", 5),
		"2026-11-01": mergeDay("2026-11-01", "example.org", 1, "v1", 2),
	})
	writeMergeState(t, b, saltTwo, map[string]*Day{
		"2026-11-10": mergeDay("2026-11-10", "example.org", 2, "v1", 3),
	})
	writeMergeState(t, c, saltOne, map[string]*Day{
		"2026-11-10": mergeDay("2026-11-10", "example.org", 4, "v1", 4),
	})
	la, _ := loadState(a)
	lb, _ := loadState(b)
	lc, _ := loadState(c)

	// Distinct salts: the same client id stays two distinct visitors/clients.
	days := combineDays([]*State{la, lb})
	if ranked := buildData(Report{Days: days}).TopPathsBytes; len(ranked) != 1 || ranked[0].Count != 600 {
		t.Fatalf("saved path traffic was lost during merge: %+v", ranked)
	}
	req, byt, vis, cli, countries := mergeTotals(days)
	if req != 6 || byt != 600 {
		t.Fatalf("sums = %d/%d, want 6/600", req, byt)
	}
	if vis != 2 || cli != 2 {
		t.Fatalf("distinct-salt union = %d visitors/%d clients, want 2/2", vis, cli)
	}
	if countries != 10 {
		t.Fatalf("countries = %d, want 10", countries)
	}
	// Retention is manual, so every stored day survives the merge: 2 from a
	// (2026-11-10, 2026-11-01) and 1 from b.
	if len(days) != 3 {
		t.Fatalf("days = %d, want 3", len(days))
	}

	// Same salt: identical visitor ids union to one.
	days = combineDays([]*State{la, lc})
	_, _, vis, cli, _ = mergeTotals(days)
	if vis != 1 || cli != 1 {
		t.Fatalf("same-salt union = %d visitors/%d clients, want 1/1", vis, cli)
	}
}

// UTC snapshots merge regardless of the display timezone; snapshots that
// stored buckets in another zone are rejected before any write.
func TestMergeSnapshotTimezones(t *testing.T) {
	dir := t.TempDir()
	writeMergeState(t, filepath.Join(dir, "a.json"), saltOne, map[string]*Day{"2026-11-10": mergeDay("2026-11-10", "example.org", 3, "v1", 5)})
	writeMergeState(t, filepath.Join(dir, "b.json"), saltTwo, map[string]*Day{"2026-11-10": mergeDay("2026-11-10", "example.org", 2, "v1", 3)})
	// UTC states merge under any display zone.
	site := filepath.Join(dir, "site")
	if err := os.MkdirAll(site, 0755); err != nil {
		t.Fatal(err)
	}
	withLocal(t, "Europe/Berlin", func() {
		err := renderStateReports([]string{dir + "/a.json", dir + "/b.json"}, filepath.Join(site, "index.html"), time.Now(), "ocean", false, false, false, io.Discard)
		if err != nil {
			t.Fatalf("UTC snapshots rejected under Berlin: %v", err)
		}
		if _, err := os.Stat(filepath.Join(site, "index.html")); err != nil {
			t.Fatal(err)
		}
	})
	// A snapshot saved with a non-UTC bucket zone is rejected.
	lc, err := loadState(filepath.Join(dir, "a.json"))
	if err != nil {
		t.Fatal(err)
	}
	lc.Timezone = "Europe/Berlin"
	b, err := json.Marshal(lc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	if err := renderStateReports([]string{dir + "/a.json"}, filepath.Join(dir, "site", "index.html"), time.Now(), "ocean", false, false, false, io.Discard); err == nil || !strings.Contains(err.Error(), "replay") {
		t.Fatalf("non-UTC snapshot accepted: %v", err)
	}
}

func TestRenderStateReportsPublish(t *testing.T) {
	testRenderStateReportsPublish(t)
}

func testRenderStateReportsPublish(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	a := filepath.Join(dir, "a.json")
	b := filepath.Join(dir, "b.json")
	writeMergeState(t, a, saltOne, map[string]*Day{"2026-11-10": mergeDay("2026-11-10", "example.org", 3, "v1", 5)})
	writeMergeState(t, b, saltTwo, map[string]*Day{"2026-11-10": mergeDay("2026-11-10", "other.org", 2, "v2", 3)})
	beforeA, _ := os.ReadFile(a)
	beforeB, _ := os.ReadFile(b)
	site := filepath.Join(dir, "site")
	if err := os.MkdirAll(site, 0755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(site, "index.html")
	var buf bytes.Buffer
	err := renderStateReports([]string{a, b}, out, now, "forest", false, false, false, &buf)
	if err != nil {
		t.Fatal(err)
	}
	// index + all-sites year + all-sites month + two vhost pages + per-vhost
	// sub-pages (each host has one year and one month, so four sub-pages).
	pages, err := filepath.Glob(filepath.Join(site, "*.html"))
	if err != nil || len(pages) != 9 {
		t.Fatalf("published pages = %d, want index + all-sites + vhost + sub-pages: %v", len(pages), err)
	}
	afterA, _ := os.ReadFile(a)
	afterB, _ := os.ReadFile(b)
	if !bytes.Equal(beforeA, afterA) || !bytes.Equal(beforeB, afterB) {
		t.Fatal("source state file mutated")
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"index-year-2026.html", "index-month-2026-11.html"} {
		if _, err := os.Stat(filepath.Join(site, f)); err != nil {
			t.Fatalf("detail page %s missing: %v", f, err)
		}
	}
	page, _ := os.ReadFile(out)
	if !strings.Contains(string(page), "All sites") {
		t.Fatal("index page does not look like a report")
	}

	// Same file twice (alias) is rejected.
	fail := func(err error, substr, what string) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), substr) {
			t.Fatalf("%s not rejected: %v", what, err)
		}
	}
	if err := renderStateReports([]string{a, a}, out, now, "ocean", false, false, false, io.Discard); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate input not rejected: %v", err)
	}
	// A hardlinked output alias of a source state is rejected.
	alias := filepath.Join(site, "alias.html")
	if err := os.Link(a, alias); err != nil {
		t.Fatal(err)
	}
	err = renderStateReports([]string{a, b}, alias, now, "ocean", false, false, false, io.Discard)
	fail(err, "overlaps", "output alias of source state")
	// A source's lock file is a reserved output target even when absent.
	err = renderStateReports([]string{a, b}, a+".lock", now, "ocean", false, false, false, io.Discard)
	fail(err, "overlap", "output path of source lock")
	// Missing and malformed sources fail.
	err = renderStateReports([]string{a, filepath.Join(dir, "missing.json")}, out, now, "ocean", false, false, false, io.Discard)
	fail(err, "no such file", "missing source")
	bad := filepath.Join(dir, "bad.json")
	writeTest(t, bad, "{not json")
	err = renderStateReports([]string{bad}, out, now, "ocean", false, false, false, io.Discard)
	fail(err, "invalid state", "malformed source")
}
