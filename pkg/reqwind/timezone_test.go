package reqwind

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withLocal runs f with time.Local forced to a fixed zone. Bucket storage is
// UTC; these tests prove the display zone never shifts stored buckets. Tests
// in this package are serial by design (no t.Parallel anywhere).
func withLocal(t *testing.T, name string, f func()) {
	t.Helper()
	previous := time.Local
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("zone %s unavailable: %v", name, err)
	}
	time.Local = loc
	defer func() { time.Local = previous }()
	f()
}

func newTestMAC(salt string) hash.Hash {
	return hmac.New(sha256.New, []byte(salt))
}

// Fresh states omit the Timezone field and always bucket in UTC, no matter
// which display timezone is active.
func TestIngestIsUTCRegardlessOfDisplayZone(t *testing.T) {
	withLocal(t, "Europe/Berlin", func() {
		dir := t.TempDir()
		path := filepath.Join(dir, "log")
		now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
		// 2026-11-12 23:30 UTC is 2026-11-13 00:30 Berlin: it must stay in
		// the UTC day 12, hour 23. 2026-01-31 23:00 UTC stays in 2026-01.
		writeTest(t, path,
			logLine(time.Date(2026, 11, 12, 23, 30, 0, 0, time.UTC), "/midnight")+
				logLine(time.Date(2026, 1, 31, 23, 0, 0, 0, time.UTC), "/monthcut"))
		s, err := loadState(filepath.Join(dir, "state"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.ingest(path, now, "site"); err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte(`"Timezone"`)) {
			t.Fatalf("new state must omit the Timezone field: %s", b)
		}
		for _, d := range s.days() {
			switch d.Date {
			case "2026-11-12":
				if d.Hours[23] != 1 || d.Requests != 1 {
					t.Fatalf("midnight hit left its UTC day/hour: %+v", d)
				}
			case "2026-01-31":
				if d.Requests != 1 {
					t.Fatalf("month cutoff hit left its UTC day: %+v", d)
				}
			default:
				t.Fatalf("unexpected day %s: %+v", d.Date, d)
			}
		}
		if _, ok := s.Months["2026-02"]; ok {
			t.Fatal("January hit bucketed in February (display zone leaked)")
		}
	})
}

// Appending to the same state under a changed display timezone succeeds;
// stored day and hour buckets stay UTC.
func TestAppendSucceedsWhenDisplayTZChanges(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	withLocal(t, "Europe/Berlin", func() {
		s, err := loadState(state)
		if err != nil {
			t.Fatal(err)
		}
		at := time.Date(2026, 11, 12, 23, 50, 0, 0, time.UTC)
		if err := s.add(metricEntry(at, "192.0.2.1", "/"), "2026-11-14", newTestMAC(s.Salt)); err != nil {
			t.Fatal(err)
		}
		d := s.days()[0]
		if d.Date != "2026-11-12" || d.Visits != 1 {
			t.Fatalf("visit not on its UTC day: %+v", d)
		}
		if err := s.saveState(state); err != nil {
			t.Fatal(err)
		}
	})
	withLocal(t, "America/New_York", func() {
		s, err := loadState(state)
		if err != nil {
			t.Fatal(err)
		}
		at := time.Date(2026, 11, 12, 23, 40, 0, 0, time.UTC)
		if err := s.add(metricEntry(at, "192.0.2.1", "/"), "2026-11-14", newTestMAC(s.Salt)); err != nil {
			t.Fatalf("display TZ change must not block append: %v", err)
		}
		// The new hit is 11:40 on Nov 13 New York; it must still land in
		// the UTC day 12, hour 23, beside the Berlin-era data.
		var d Day
		for _, day := range s.days() {
			if day.Date == "2026-11-12" {
				d = day
			}
		}
		if d.Date != "2026-11-12" || d.Visits != 1 || d.Hours[23] != 2 || d.Requests != 2 {
			t.Fatalf("buckets shifted by display TZ: %+v", d)
		}
	})
}

// Stored day buckets stay anchored to the UTC date even when the display
// zone crosses DST or starts on a non-UTC hour.
func TestDSTBucketingStaysUTC(t *testing.T) {
	withLocal(t, "Europe/Berlin", func() {
		s, err := loadState(filepath.Join(t.TempDir(), "state"))
		if err != nil {
			t.Fatal(err)
		}
		// A hit at 23:20 UTC = 00:20 Berlin on 03-28 stays on the UTC day
		// 2026-03-27.
		old := time.Date(2026, 3, 27, 23, 20, 0, 0, time.UTC)
		if err := s.add(metricEntry(old, "192.0.2.1", "/"), "2026-03-28", newTestMAC(s.Salt)); err != nil {
			t.Fatal(err)
		}
		kept := time.Date(2026, 3, 28, 0, 0, 0, 0, time.UTC)
		if err := s.add(metricEntry(kept, "192.0.2.1", "/"), "2026-03-28", newTestMAC(s.Salt)); err != nil {
			t.Fatal(err)
		}
		var d Day
		for _, day := range s.days() {
			if day.Date == "2026-03-28" {
				d = day
			}
		}
		if d.Date != "2026-03-28" || d.Visits != 1 {
			t.Fatalf("visit not on its UTC start day: %+v", d)
		}
	})
}

// A state that stored buckets in a non-UTC zone is rejected with an
// actionable new-state/replay instruction. The file is never deleted or
// rewritten.
func TestNonUTCStateRejectedWithReplayInstruction(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	log := filepath.Join(dir, "log")
	var before []byte
	s, err := loadState(state)
	if err != nil {
		t.Fatal(err)
	}
	s.Timezone = "Europe/Berlin"
	s.ensureDay("2026-11-12", "site").Requests = 1
	before, err = json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	writeTest(t, state, string(before))
	writeTest(t, log, logLine(time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC), "/new"))
	// Rejected at load, so no write happens.
	_, err = loadState(state)
	if err == nil || !strings.Contains(err.Error(), "replay") {
		t.Fatalf("non-UTC state not rejected: %v", err)
	}
	after, err := os.ReadFile(state)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("rejected state file changed: %v", err)
	}
}

// A UTC state accepts any display timezone, and the Timezone field may be
// empty or the alias "UTC".
func TestUTCStateAcceptsAnyDisplayZone(t *testing.T) {
	for _, tz := range []string{"", "UTC", "Etc/UTC", "Etc/GMT", "GMT"} {
		withLocal(t, "Europe/Berlin", func() {
			path := filepath.Join(t.TempDir(), "state")
			s, err := loadState(path)
			if err != nil {
				t.Fatal(err)
			}
			s.Timezone = tz
			if err := atomicWrite(path, 0600, func(w io.Writer) error { return json.NewEncoder(w).Encode(s) }); err != nil {
				t.Fatal(err)
			}
			if _, err := loadState(path); err != nil {
				t.Fatalf("UTC alias %q rejected: %v", tz, err)
			}
		})
	}
}

// New states omit the Timezone field and validate immediately.
func TestNewStateOmitsTimezone(t *testing.T) {
	withLocal(t, "Europe/Berlin", func() {
		s, err := loadState(filepath.Join(t.TempDir(), "state"))
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte(`"Timezone"`)) {
			t.Fatalf("new state persists the Timezone field: %s", b)
		}
		at := time.Date(2026, 11, 12, 23, 50, 0, 0, time.UTC)
		if err := s.add(metricEntry(at, "192.0.2.1", "/"), "2026-11-14", newTestMAC(s.Salt)); err != nil {
			t.Fatal(err)
		}
		d := s.days()[0]
		if d.Date != "2026-11-12" || d.Visits != 1 {
			t.Fatalf("fresh state not bucketed in UTC: %+v", d)
		}
	})
}
