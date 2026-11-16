package reqwind

import (
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

func metricEntry(at time.Time, host, path string) Entry {
	return Entry{Time: at, Host: host, Vhost: "example.org", Method: "GET", Path: path, Status: 200, Bytes: 10}
}

func addMetricEntries(t *testing.T, s *State, entries []Entry) {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(s.Salt))
	for _, e := range entries {
		// A far-future today stores every entry; retention is manual, so
		// nothing is dropped on the way in.
		if err := s.add(e, "9999-12-31", mac); err != nil {
			t.Fatal(err)
		}
	}
}

func TestVisitOrderIndependenceRegression(t *testing.T) {
	base := time.Date(2026, 11, 12, 23, 50, 0, 0, time.UTC)
	minutes := []int{0, 20, 40, 60}
	var permute func([]int, int)
	permute = func(order []int, i int) {
		if i < len(order) {
			for j := i; j < len(order); j++ {
				order[i], order[j] = order[j], order[i]
				permute(order, i+1)
				order[i], order[j] = order[j], order[i]
			}
			return
		}
		s, err := loadState(filepath.Join(t.TempDir(), "state"))
		if err != nil {
			t.Fatal(err)
		}
		var entries []Entry
		for _, n := range order {
			entries = append(entries, metricEntry(base.Add(time.Duration(minutes[n])*time.Minute), "192.0.2.1", "/"))
		}
		addMetricEntries(t, s, entries)
		var visits int64
		for _, d := range s.days() {
			visits += d.Visits
			if d.Visits < 0 || d.Files != d.Requests || d.Pages != d.Requests {
				t.Fatalf("order%v bad day %+v", order, d)
			}
			if d.Date == "2026-11-13" && d.Visits != 0 {
				t.Fatalf("order%v attributed visit after midnight: %d", order, d.Visits)
			}
		}
		if visits != 1 || requests(s) != 4 {
			t.Fatalf("order%v visits=%d requests=%d", order, visits, requests(s))
		}
		for _, intervals := range s.Sessions["example.org"] {
			if len(intervals) != 1 || intervals[0].Start != base.Unix() || intervals[0].End != base.Add(time.Hour).Unix() {
				t.Fatalf("order%v intervals=%+v", order, intervals)
			}
		}
	}
	permute([]int{0, 1, 2, 3}, 0)
}

func TestClientCountersIncludeNonPagesRegression(t *testing.T) {
	s, err := loadState(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	entries := []Entry{metricEntry(at, "192.0.2.1", "/asset.css"), metricEntry(at, "192.0.2.1", "/error.html"), metricEntry(at, "192.0.2.1", "/head.html"), metricEntry(at, "192.0.2.1", "/v1.0/route")}
	entries[1].Status = 404
	entries[2].Method = "HEAD"
	entries[2].Bytes = 0
	addMetricEntries(t, s, entries)
	d := s.days()[0]
	if d.Files != 2 || d.Pages != 1 || d.Visits != 1 {
		t.Fatalf("wrong metrics: %+v", d)
	}
	for _, c := range d.Clients {
		if c.Requests != 4 || c.Bytes != 30 {
			t.Fatalf("non-page clients excluded: %+v", c)
		}
	}
}

func TestManyClientsRemainExactRegression(t *testing.T) {
	s, err := loadState(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	d := s.ensureDay("2026-11-13", "example.org")
	for i := 0; i < maxKeys; i++ {
		d.Clients[fmt.Sprintf("%032x", i)] = ClientStats{}
	}
	at := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	addMetricEntries(t, s, []Entry{metricEntry(at, "192.0.2.1", "/"), metricEntry(at, "192.0.2.2", "/")})
	if d.Visits != 2 || len(s.Sessions["example.org"]) != 2 {
		t.Fatal("overflow clients collapsed into one visit")
	}
	var hits, traffic int64
	for _, client := range d.Clients {
		hits += client.Requests
		traffic += client.Bytes
	}
	if len(d.Clients) != maxKeys+2 || hits != 2 || traffic != 20 {
		t.Fatalf("client rankings lost data: clients%d hits%d traffic%d", len(d.Clients), hits, traffic)
	}
}

func TestSessionPersistenceAndVhostSeparation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")
	s, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	addMetricEntries(t, s, []Entry{metricEntry(at, "192.0.2.1", "/")})
	if err := s.saveState(path); err != nil {
		t.Fatal(err)
	}
	s, err = loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	second := metricEntry(at.Add(20*time.Minute), "192.0.2.1", "/")
	other := second
	other.Vhost = "other.example"
	addMetricEntries(t, s, []Entry{second, other})
	var visits int64
	for _, d := range s.days() {
		visits += d.Visits
		if d.Visits != 1 {
			t.Fatalf("vhost session count: %+v", d)
		}
	}
	if visits != 2 || requests(s) != 3 || len(s.Sessions) != 2 {
		t.Fatal("session persistence or vhost isolation failed")
	}
}

func TestSessionUpdateDoesNotRecreateRemovedMonth(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	s, err := loadState(state)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 1, 31, 23, 50, 0, 0, time.UTC)
	addMetricEntries(t, s, []Entry{metricEntry(at, "192.0.2.1", "/")})
	if err := s.saveState(state); err != nil {
		t.Fatal(err)
	}
	removed := state + "-2026-01.json"
	if err := os.Remove(removed); err != nil {
		t.Fatal(err)
	}
	s, err = loadState(state)
	if err != nil {
		t.Fatal(err)
	}
	addMetricEntries(t, s, []Entry{metricEntry(at.Add(20*time.Minute), "192.0.2.1", "/")})
	if _, ok := s.Months["2026-01"]; ok {
		t.Fatal("session update recreated the removed month")
	}
	if err := s.saveState(state); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(removed); !os.IsNotExist(err) {
		t.Fatalf("removed bucket was written: %v", err)
	}
	if days := s.days(); len(days) != 1 || days[0].Requests != 1 || days[0].Visits != 0 {
		t.Fatalf("wrong retained totals: %+v", days)
	}
}

func TestSessionMovePersistsBothMonths(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	s, err := loadState(state)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 2, 1, 0, 10, 0, 0, time.UTC)
	addMetricEntries(t, s, []Entry{metricEntry(at, "192.0.2.1", "/")})
	if err := s.saveState(state); err != nil {
		t.Fatal(err)
	}
	s, err = loadState(state)
	if err != nil {
		t.Fatal(err)
	}
	addMetricEntries(t, s, []Entry{metricEntry(at.Add(-20*time.Minute), "192.0.2.1", "/")})
	if err := s.saveState(state); err != nil {
		t.Fatal(err)
	}
	s, err = loadState(state)
	if err != nil {
		t.Fatal(err)
	}
	days := s.days()
	if len(days) != 2 || days[0].Visits != 1 || days[1].Visits != 0 {
		t.Fatalf("moved session was not saved in both months: %+v", days)
	}
}

func TestPageClassification(t *testing.T) {
	at := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		path, method string
		status       int
		page         bool
	}{
		{"/", "GET", 200, true}, {"/index.HTML", "GET", 200, true}, {"/v1.0/route", "POST", 201, true},
		{"/script.php", "GET", 200, true}, {"/.git", "GET", 200, false}, {"/app.js", "GET", 200, false},
		{"/index.html", "HEAD", 200, false}, {"/index.html", "GET", 304, false}, {"/index.html", "GET", 404, false},
		{"", "GET", 200, false},
	} {
		e := metricEntry(at, "192.0.2.1", tc.path)
		e.Method, e.Status = tc.method, tc.status
		if got := isPage(e); got != tc.page {
			t.Fatalf("%s %s %d: page=%t want%t", tc.method, tc.path, tc.status, got, tc.page)
		}
	}
}

func TestVisitMergesAgainstSortedBatch(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	base := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	for run := 0; run < 50; run++ {
		var timestamps []int64
		for i := 0; i < 20; i++ {
			timestamps = append(timestamps, base.Add(time.Duration(rng.Intn(72*60))*time.Minute).Unix())
		}
		sort.Slice(timestamps, func(i, j int) bool { return timestamps[i] < timestamps[j] })
		var want []Visit
		starts := map[string]int64{}
		for _, ts := range timestamps {
			if len(want) == 0 || ts > want[len(want)-1].End+visitGap {
				want = append(want, Visit{Start: ts, End: ts})
				starts[time.Unix(ts, 0).UTC().Format("2006-01-02")]++
			} else {
				want[len(want)-1].End = ts
			}
		}
		rng.Shuffle(len(timestamps), func(i, j int) { timestamps[i], timestamps[j] = timestamps[j], timestamps[i] })
		s, err := loadState(filepath.Join(t.TempDir(), "state"))
		if err != nil {
			t.Fatal(err)
		}
		var entries []Entry
		for _, ts := range timestamps {
			entries = append(entries, metricEntry(time.Unix(ts, 0), "192.0.2.1", "/"))
		}
		addMetricEntries(t, s, entries)
		for _, got := range s.Sessions["example.org"] {
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("run%d: intervals%+v want%+v", run, got, want)
			}
		}
		for _, d := range s.days() {
			if d.Visits != starts[d.Date] {
				t.Fatalf("run%d date%s: visits%d want%d", run, d.Date, d.Visits, starts[d.Date])
			}
		}
	}
}
