package reqwind

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMergeStateCLI(t *testing.T) {
	mergeStates(t, func() { testMergeStateCLI(t) })
}

func testMergeStateCLI(t *testing.T) {
	dir := t.TempDir()
	for i, name := range []string{"one.json", "two.json"} {
		path := filepath.Join(dir, name)
		s, err := loadState(path)
		if err != nil {
			t.Fatal(err)
		}
		d := s.ensureDay(time.Now().Local().Format("2006-01-02"), "example.org")
		d.Requests = int64(i + 1)
		d.Visitors["client"] = true
		// saveState writes the meta file plus month buckets; the *.json glob
		// matches the buckets, but renderStateReports skips them.
		if err := s.saveState(path); err != nil {
			t.Fatal(err)
		}
	}
	output, unused := filepath.Join(dir, "index.html"), filepath.Join(dir, "unused-state")
	args := []string{"-merge-state", filepath.Join(dir, "*.json"), "-output", output, "-state", unused, "-palette", "ember", "-geo-db", "/missing/database"}
	if err := Run(context.Background(), args, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`<span class="num" title="3">3</span>`, `<span class="num" title="2">2</span>`, `data-palette="ember"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("merged report missing %q", want)
		}
	}
	for _, path := range []string{unused, unused + ".lock"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("snapshot mode wrote unused state path %s: %v", path, err)
		}
	}
}

func TestMergeStateRejectsLogModes(t *testing.T) {
	for _, extra := range [][]string{{"-live"}, {"-vhost", "site=access.log"}, {"access.log"}} {
		args := append([]string{"-merge-state", "missing.json"}, extra...)
		if err := Run(context.Background(), args, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
			t.Errorf("%v: expected incompatible mode error, got %v", args, err)
		}
	}
}

func TestRejectsNonUTCStateBeforeEmptyDiscovery(t *testing.T) {
	dir := t.TempDir()
	path, output := filepath.Join(dir, "state.json"), filepath.Join(dir, "index.html")
	var before []byte
	withLocal(t, "UTC", func() {
		s, err := loadState(path)
		if err != nil {
			t.Fatal(err)
		}
		s.ensureDay("2020-01-01", "example.org").Requests = 1
		s.Timezone = "Europe/Berlin"
		before, err = json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		writeTest(t, path, string(before))
	})
	previous := defaultLogBases
	defaultLogBases = nil
	t.Cleanup(func() { defaultLogBases = previous })
	withLocal(t, "Europe/Berlin", func() {
		err := Run(context.Background(), []string{"-state", path, "-output", output, "-geo-db", "off"}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "replay") {
			t.Fatalf("expected timezone error before pruning, got %v", err)
		}
	})
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("timezone mismatch changed state: %v", err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("timezone mismatch published output: %v", err)
	}
}

func TestChangingDisplayTimezoneKeepsUTCState(t *testing.T) {
	dir := t.TempDir()
	state, log := filepath.Join(dir, "state.json"), filepath.Join(dir, "access.log")
	writeTest(t, log, logLine(time.Date(2026, 11, 12, 23, 30, 0, 0, time.UTC), "/utc"))
	args := []string{"-state", state, "-output", filepath.Join(dir, "index.html"), "-geo-db", "off", log}
	withLocal(t, "UTC", func() {
		if err := Run(context.Background(), args, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	})
	withLocal(t, "Europe/Berlin", func() {
		if err := Run(context.Background(), args, &bytes.Buffer{}); err != nil {
			t.Fatalf("changing display timezone required a state rebuild: %v", err)
		}
	})
	s, err := loadState(state)
	if err != nil {
		t.Fatal(err)
	}
	days := s.days()
	if len(days) != 1 || days[0].Date != "2026-11-12" || days[0].Requests != 1 || days[0].Hours[23] != 1 {
		t.Fatalf("display timezone changed stored UTC data: %+v", days)
	}
}

func TestCLIRejectsUnsupportedTZBeforeWriting(t *testing.T) {
	for _, tz := range []string{"+05:30", "Unknown/Timezone"} {
		t.Run(tz, func(t *testing.T) {
			t.Setenv("TZ", tz)
			dir := t.TempDir()
			state := filepath.Join(dir, "state.json")
			err := Run(context.Background(), []string{"-state", state, "-output", filepath.Join(dir, "index.html"), filepath.Join(dir, "missing.log")}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), "invalid TZ") {
				t.Fatalf("unsupported TZ was silently accepted: %v", err)
			}
			if _, err := os.Stat(state + ".lock"); !os.IsNotExist(err) {
				t.Fatalf("invalid TZ created a lock: %v", err)
			}
		})
	}
}
