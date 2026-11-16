package reqwind

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func logLine(at time.Time, path string) string {
	return fmt.Sprintf("127.0.0.1 - - [%s] \"GET %s HTTP/1.1\" 200 42 \"https://example.org/?secret=yes\" \"test\"\n", at.Format("02/Jan/2006:15:04:05 -0700"), path)
}

func writeTest(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}
func requests(s *State) int64 {
	var n int64
	for _, d := range s.days() {
		n += d.Requests
	}
	return n
}

func TestErrorPathsIngestPersistAndMerge(t *testing.T) {
	dir := t.TempDir()
	log, state := filepath.Join(dir, "access.log"), filepath.Join(dir, "state.json")
	now := time.Now()
	line := func(path, status string) string {
		return strings.Replace(logLine(now, path), " 200 42 ", " "+status+" 42 ", 1)
	}
	writeTest(t, log, line("/mixed", "200")+line("/mixed", "404")+line("/mixed", "503")+line("/mixed", "401")+line("/login", "401")+line("/redirect", "302")+strings.Repeat(line("/bad<script>", "400"), 10))
	args := []string{"-state", state, "-output", filepath.Join(dir, "index.html"), "-geo-db", "off", log}
	for i := 0; i < 2; i++ {
		if err := Run(context.Background(), args, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	s, err := loadState(state)
	if err != nil {
		t.Fatal(err)
	}
	days := combineDays([]*State{s, s})
	d := buildData(Report{Days: days, ShowDrilldown: true})
	if len(d.TopErrorPaths) != 2 || d.TopErrorPaths[0] != (top{"/bad<script>", 20}) || d.TopErrorPaths[1] != (top{"/mixed", 4}) {
		t.Fatalf("failed path counts lost or recounted: %+v", d.TopErrorPaths)
	}
	if len(d.TopPaths) != 4 || d.T.Requests != 32 {
		t.Fatal("error filtering changed the overall request totals")
	}
	// 3xx responses are counted per path so the drilldown can drop them.
	if day := s.days()[0]; day.RedirectPaths["/redirect"] != 1 {
		t.Fatalf("redirect path counts missing: %+v", day.RedirectPaths)
	}
	if got := s.days()[0].Status["401"]; got != 2 {
		t.Fatalf("401 status count = %d, want 2", got)
	}
	// Failures (401/404/503) and redirects (302) stay out of the drilldown.
	if d.PathDrilldown != pathDrilldown(map[string]int64{"/mixed": 2}) {
		t.Fatal("failures or redirects leaked into the drilldown")
	}
	out := mustRender(t, Report{Days: days})
	if !strings.Contains(out, "/bad&lt;script&gt;") || strings.Contains(out, "/bad<script>") {
		t.Fatal("error path was not escaped")
	}
}

func TestIncrementalRotationAndTruncation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access.log")
	now := time.Now()
	line := logLine(now, "/a?token=secret")
	s, err := loadState(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	writeTest(t, path, line+line[:20])
	scan := func(want int64) {
		t.Helper()
		if _, err := s.ingest(path, now, "site"); err != nil {
			t.Fatal(err)
		}
		if n := requests(s); n != want {
			t.Fatalf("requests=%d want %d", n, want)
		}
	}
	scan(1)
	scan(1)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(line[20:]); err != nil {
		t.Fatal(err)
	}
	f.Close()
	scan(2)
	rotated := path + ".1"
	if err := os.Rename(path, rotated); err != nil {
		t.Fatal(err)
	}
	writeTest(t, path, logLine(now, "/b"))
	scan(3)
	if _, err := s.ingest(rotated, now, "site"); err != nil {
		t.Fatal(err)
	}
	if requests(s) != 3 {
		t.Fatal("rotation counted twice")
	}
	writeTest(t, path, logLine(now, "/c"))
	scan(4)
	for _, d := range s.days() {
		if d.Paths["/a"] != 2 || d.Referrers["https://example.org/"] != 4 {
			t.Fatalf("privacy stripping failed: %+v", d)
		}
		if d.PathBytes["/a"] != 84 || d.PathBytes["/b"] != 42 || d.PathBytes["/c"] != 42 {
			t.Fatalf("path traffic counted incorrectly: %v", d.PathBytes)
		}
		for host := range d.Visitors {
			if strings.Contains(host, "127.0") {
				t.Fatal("raw IP retained")
			}
		}
	}
}

func TestRetentionAndMalformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log")
	now := time.Now()
	writeTest(t, path, logLine(now.AddDate(0, 0, -3), "/old")+logLine(now, "/new")+"invalid\n"+strings.Repeat("x", maxLine+10)+"\n"+logLine(now.AddDate(0, 0, 1), "/future"))
	s, _ := loadState(filepath.Join(dir, "state"))
	st, err := s.ingest(path, now, "site")
	if err != nil {
		t.Fatal(err)
	}
	// Retention is manual: the past day is kept, only the future-dated line
	// is skipped. Invalid lines are still counted.
	if requests(s) != 2 || st.Invalid != 2 || st.Lines != 5 {
		t.Fatalf("%+v requests=%d", st, requests(s))
	}
}

func TestSummaryReportsOldBytesAndElapsed(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	state, html := filepath.Join(dir, "state"), filepath.Join(dir, "index.html")
	writeTest(t, log, logLine(time.Now(), "/a")+logLine(time.Now(), "/b"))
	args := []string{"-state", state, "-output", html, log}
	var first bytes.Buffer
	if err := Run(context.Background(), args, &first); err != nil {
		t.Fatal(err)
	}
	var second bytes.Buffer
	if err := Run(context.Background(), args, &second); err != nil {
		t.Fatal(err)
	}
	s, err := loadState(state)
	if err != nil {
		t.Fatal(err)
	}
	var old, oldLines int64
	for _, cp := range s.Files {
		old += cp.Offset
		oldLines += cp.TotalLines
	}
	if old == 0 || oldLines == 0 {
		t.Fatal("second run should have prior checkpointed lines and bytes")
	}
	line := second.String()
	for _, want := range []string{"new lines", "old lines", "invalid", "new decoded bytes", "old decoded bytes", "in "} {
		if !strings.Contains(line, want) {
			t.Fatalf("summary missing %q: %s", want, line)
		}
	}
	if !strings.Contains(line, fmt.Sprintf("%d old lines", oldLines)) {
		t.Fatalf("old lines wrong: %s", line)
	}
	// The skipped (old) decoded bytes equal the bytes read on the first run.
	if !strings.Contains(line, fmt.Sprintf("%d old decoded bytes", old)) {
		t.Fatalf("old decoded bytes wrong: %s", line)
	}
}

func TestCLIPersistenceAndSafety(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	state := filepath.Join(dir, "state")
	html := filepath.Join(dir, "index.html")
	writeTest(t, log, logLine(time.Now(), "/hello"))
	args := []string{"-state", state, "-output", html, log}
	var output bytes.Buffer
	if err := Run(context.Background(), args, &output); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), args, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "0 new lines") {
		t.Fatal(output.String())
	}
	s, err := loadState(state)
	if err != nil || requests(s) != 1 {
		t.Fatalf("state: %v %+v", err, s)
	}
	info, _ := os.Stat(state)
	if info.Mode().Perm() != 0600 {
		t.Fatal("state not private")
	}
	b, err := os.ReadFile(html)
	if err != nil || !bytes.Contains(b, []byte("/hello")) {
		t.Fatal("missing report", err)
	}
	if err := Run(context.Background(), []string{"-state", state, "-output", log, log}, &output); err == nil {
		t.Fatal("allowed overwrite of log")
	}
	writeTest(t, state, "broken state")
	if err := Run(context.Background(), args, &output); err == nil {
		t.Fatal("accepted corrupt state")
	}
	b, _ = os.ReadFile(state)
	if string(b) != "broken state" {
		t.Fatal("overwrote corrupt state")
	}
}

func TestMissingTargetsThroughDirectoryAlias(t *testing.T) {
	for _, detail := range []bool{false, true} {
		t.Run(fmt.Sprint("detail=", detail), func(t *testing.T) {
			dir := t.TempDir()
			alias := filepath.Join(dir, "alias")
			if err := os.Symlink(dir, alias); err != nil {
				t.Fatal(err)
			}
			log := filepath.Join(dir, "access.log")
			text := logLine(time.Now(), "/keep")
			writeTest(t, log, text)
			state := filepath.Join(dir, "state")
			output := filepath.Join(alias, "state")
			if detail {
				state = filepath.Join(dir, "index-month-"+time.Now().UTC().Format("2006-01")+".html")
				output = filepath.Join(alias, "index.html")
			}
			err := Run(context.Background(), []string{"-geo-db", "off", "-state", state, "-output", output, log}, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "overlap") {
				t.Fatalf("directory alias accepted: %v", err)
			}
			for _, path := range []string{state, output} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("target written before overlap check: %s: %v", path, err)
				}
			}
			if b, err := os.ReadFile(log); err != nil || string(b) != text {
				t.Fatalf("input changed: %v", err)
			}
		})
	}
}

func TestClientPrivacyModes(t *testing.T) {
	dir := t.TempDir()
	log, state := filepath.Join(dir, "access.log"), filepath.Join(dir, "state.json")
	index := filepath.Join(dir, "index.html")
	text := logLine(time.Now(), "/clients")
	writeTest(t, log, text)
	args := []string{"-geo-db", "off", "-state", state, "-output", index, log}
	if err := Run(context.Background(), args, io.Discard); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(state)
	if err != nil {
		t.Fatalf("read state meta: %v", err)
	}
	// The raw client address lives in the month bucket, not the meta file.
	bucket := filepath.Join(dir, filepath.Base(state)+"-"+time.Now().UTC().Format("2006-01")+".json")
	raw, err := os.ReadFile(bucket)
	if err != nil {
		t.Fatal(err)
	}
	var plain json.RawMessage
	if err := unmarshalState(raw, &plain); err != nil || !bytes.Contains(plain, []byte(`"Raw":"127.0.0.1"`)) {
		t.Fatalf("default state lacks raw client address: %v", err)
	}
	if b, err := os.ReadFile(index); err != nil || !bytes.Contains(b, []byte("127.0.0.1")) {
		t.Fatalf("default report lacks raw client address: %v", err)
	}
	for _, hash := range []bool{true, false} {
		mergeArgs := []string{"-merge-state", state, "-output", filepath.Join(dir, "merged.html")}
		if hash {
			mergeArgs = append(mergeArgs, "-hash-clients")
		}
		if err := Run(context.Background(), mergeArgs, io.Discard); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(dir, "merged.html"))
		if err != nil || bytes.Contains(b, []byte("127.0.0.1")) == hash {
			t.Fatalf("snapshot privacy mode hash=%t: %v", hash, err)
		}
		after, err := os.ReadFile(state)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("snapshot mode changed source state: %v", err)
		}
	}
	// Hash mode must remove saved raw labels even without new log data.
	if err := Run(context.Background(), append([]string{"-hash-clients"}, args...), io.Discard); err != nil {
		t.Fatal(err)
	}
	// It must also avoid storing addresses from new requests.
	writeTest(t, log, text+strings.Replace(logLine(time.Now(), "/new-client"), "127.0.0.1", "192.0.2.2", 1))
	if err := Run(context.Background(), append([]string{"-hash-clients"}, args...), io.Discard); err != nil {
		t.Fatal(err)
	}
	s, err := loadState(state)
	if err != nil || requests(s) != 2 {
		t.Fatalf("privacy switch changed totals: %v", err)
	}
	for _, day := range s.days() {
		for id, client := range day.Clients {
			if client.Raw != "" || len(id) != 32 {
				t.Fatalf("raw address retained in hash mode: %q %+v", id, client)
			}
		}
	}
	if b, err := os.ReadFile(index); err != nil || bytes.Contains(b, []byte("127.0.0.1")) || bytes.Contains(b, []byte("192.0.2.2")) {
		t.Fatalf("hash report exposes raw client address: %v", err)
	}
}

func TestVhostNamesAndPorts(t *testing.T) {
	now := time.Now()
	for _, mapping := range []string{"", "chosen.example", "chosen.example:8443"} {
		t.Run("mapping="+mapping, func(t *testing.T) {
			dir := t.TempDir()
			log, state := filepath.Join(dir, "access.log"), filepath.Join(dir, "state")
			text := "shop.example:80 " + logLine(now, "/one") + "shop.example:443 " + logLine(now, "/two")
			if mapping != "" {
				text += "other.example:443 " + logLine(now, "/three") + logLine(now, "/four")
			}
			writeTest(t, log, text)
			args := []string{"-geo-db", "off", "-state", state, "-output", filepath.Join(dir, "index.html")}
			if mapping == "" {
				args = append(args, log)
			} else {
				args = append(args, "-vhost", mapping+"="+log)
			}
			for i := 0; i < 2; i++ {
				if err := Run(context.Background(), args, io.Discard); err != nil {
					t.Fatal(err)
				}
			}
			s, err := loadState(state)
			if err != nil {
				t.Fatal(err)
			}
			want, hits := mapping, int64(4)
			if mapping == "" {
				want, hits = "shop.example", 2
			}
			days := s.days()
			if len(days) != 1 || days[0].Vhost != want || days[0].Requests != hits {
				t.Fatalf("wrong vhost groups: %+v", days)
			}
			pages := BuildPages(filepath.Join(dir, "index.html"), s.report(now, false))
			if hosts := pages[0].Report.Vhosts; len(hosts) != 1 || hosts[0].Name != want {
				t.Fatalf("explicit vhost was guessed or renamed: %+v", hosts)
			}
		})
	}
	// Ports stay separate when both names come from command-line mappings.
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.log"), filepath.Join(dir, "b.log")
	writeTest(t, a, logLine(now, "/one"))
	writeTest(t, b, logLine(now, "/two"))
	state := filepath.Join(dir, "state")
	args := []string{"-geo-db", "off", "-state", state, "-output", filepath.Join(dir, "index.html"), "-vhost", "shop.example:80=" + a, "-vhost", "shop.example:443=" + b}
	if err := Run(context.Background(), args, io.Discard); err != nil {
		t.Fatal(err)
	}
	s, err := loadState(state)
	if err != nil {
		t.Fatal(err)
	}
	days := s.days()
	if len(days) != 2 || days[0].Vhost != "shop.example:443" || days[1].Vhost != "shop.example:80" {
		t.Fatalf("explicit ports were merged: %+v", days)
	}
}

func TestLive(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	state := filepath.Join(dir, "state")
	html := filepath.Join(dir, "index.html")
	writeTest(t, log, logLine(time.Now(), "/first"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, []string{"-live", "-interval", "100ms", "-state", state, "-output", html, log}, &bytes.Buffer{})
	}()
	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	appended := false
	for {
		select {
		case err := <-done:
			t.Fatalf("live exited early: %v", err)
		case <-deadline:
			t.Fatal("live report did not update")
		case <-ticker.C:
			b, _ := os.ReadFile(html)
			if !appended && bytes.Contains(b, []byte("/first")) {
				f, err := os.OpenFile(log, os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.WriteString(logLine(time.Now(), "/second"))
				f.Close()
				if err != nil {
					t.Fatal(err)
				}
				appended = true
			}
			if bytes.Contains(b, []byte("/second")) {
				cancel()
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				return
			}
		}
	}
}

func TestAtomicWriteFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing")
	writeTest(t, path, "keep me")
	err := atomicWrite(path, 0600, func(w io.Writer) error {
		if _, err := io.WriteString(w, "incomplete"); err != nil {
			return err
		}
		return fmt.Errorf("write failed")
	})
	if err == nil {
		t.Fatal("lost write error")
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "keep me" {
		t.Fatalf("original changed: %q %v", b, err)
	}
}

func TestBoundedLabels(t *testing.T) {
	m := map[string]int64{}
	for i := 0; i < maxKeys+100; i++ {
		bump(m, fmt.Sprint(i))
	}
	bump(m, "0")
	if len(m) != maxKeys+1 || m["(other)"] != 100 || m["0"] != 2 {
		t.Fatalf("unexpected overflow accounting: len=%d other=%d", len(m), m["(other)"])
	}
}

func TestConcurrentWriterRejected(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	lock, err := os.OpenFile(state+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	log := filepath.Join(dir, "log")
	writeTest(t, log, logLine(time.Now(), "/test"))
	err = Run(context.Background(), []string{"-state", state, "-output", filepath.Join(dir, "report"), log}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "another reqwind") {
		t.Fatalf("writer not rejected: %v", err)
	}
}

func TestMultipleSourcesAndAliases(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.log"), filepath.Join(dir, "b.log")
	alias := filepath.Join(dir, "alias.log")
	writeTest(t, a, logLine(time.Now(), "/a"))
	writeTest(t, b, logLine(time.Now(), "/b"))
	if err := os.Link(a, alias); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, "state")
	if err := Run(context.Background(), []string{"-state", state, "-output", filepath.Join(dir, "report"), filepath.Join(dir, "*.log")}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	s, err := loadState(state)
	if err != nil || requests(s) != 2 {
		t.Fatalf("multiple sources counted incorrectly: %v %+v", err, s)
	}
	if err := Run(context.Background(), []string{"-state", state, "-output", alias, a}, &bytes.Buffer{}); err == nil {
		t.Fatal("allowed overwriting hardlinked input")
	}
}

func BenchmarkIngest(b *testing.B) {
	dir := b.TempDir()
	path := filepath.Join(dir, "log")
	now := time.Now()
	data := strings.Repeat(logLine(now, "/bench"), 10000)
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s, err := loadState(filepath.Join(dir, "missing"))
		if err != nil {
			b.Fatal(err)
		}
		if _, err = s.ingest(path, now, "site"); err != nil {
			b.Fatal(err)
		}
	}
}

// The retention boundaries are anchored to UTC dates.
func TestStatePersistsPerMonthBuckets(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(dir, "log")
	state := filepath.Join(dir, "state")
	// Two requests in different months; one from two years earlier. Retention
	// is manual, so every past month is kept.
	writeTest(t, path, logLine(time.Date(2024, 4, 1, 0, 0, 0, 0, time.UTC), "/first")+logLine(now, "/latest"))
	s, err := loadState(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ingest(path, now, "example.org"); err != nil {
		t.Fatal(err)
	}
	if requests(s) != 2 || len(s.Months) != 2 {
		t.Fatalf("wrong months: %+v", s.Months)
	}
	if err := s.saveState(state); err != nil {
		t.Fatal(err)
	}
	// The meta file plus one bucket file per month exist beside it.
	for _, want := range []string{"state", "state-2026-03.json", "state-2024-04.json"} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Fatalf("missing %s: %v", want, err)
		}
	}
	// Reloaded state restores both months and their request counts.
	loaded, err := loadState(state)
	if err != nil {
		t.Fatal(err)
	}
	if requests(loaded) != 2 || len(loaded.Months) != 2 {
		t.Fatalf("round trip lost months: %+v", loaded.Months)
	}
}

func TestStateWritesOnlyChangedMonths(t *testing.T) {
	dir := t.TempDir()
	state, log := filepath.Join(dir, "state"), filepath.Join(dir, "access.log")
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	text := logLine(now.AddDate(0, -2, 0), "/jan") + logLine(now.AddDate(0, -1, 0), "/feb")
	writeTest(t, log, text)
	s, err := loadState(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ingest(log, now, "site"); err != nil {
		t.Fatal(err)
	}
	if err := s.saveState(state); err != nil {
		t.Fatal(err)
	}
	old := time.Unix(1000000000, 0)
	for _, month := range []string{"2026-01", "2026-02"} {
		path := state + "-" + month + ".json"
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	s, err = loadState(state)
	if err != nil {
		t.Fatal(err)
	}
	writeTest(t, log, text+logLine(now.AddDate(0, -1, 1), "/new"))
	if _, err := s.ingest(log, now, "site"); err != nil {
		t.Fatal(err)
	}
	if err := s.saveState(state); err != nil {
		t.Fatal(err)
	}
	for _, month := range []string{"2026-01", "2026-02"} {
		info, err := os.Stat(state + "-" + month + ".json")
		if err != nil {
			t.Fatal(err)
		}
		if info.ModTime().Equal(old) != (month == "2026-01") {
			t.Fatalf("wrong rewrite decision for %s", month)
		}
	}
	s, err = loadState(state)
	if err != nil || requests(s) != 3 {
		t.Fatalf("new requests were not saved: %v", err)
	}
}

func TestUnchangedStateKeepsPrivatePermissions(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	s, err := loadState(state)
	if err != nil {
		t.Fatal(err)
	}
	s.ensureDay("2026-01-01", "site").Requests = 1
	if err := s.saveState(state); err != nil {
		t.Fatal(err)
	}
	paths := []string{state, state + "-2026-01.json"}
	for _, path := range paths {
		if err := os.Chmod(path, 0644); err != nil {
			t.Fatal(err)
		}
	}
	s, err = loadState(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.saveState(state); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("state file is not private: %s", path)
		}
	}
}

func TestStateVersionRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log")
	state := filepath.Join(dir, "state")
	now := time.Now()
	writeTest(t, path, logLine(now, "/preserved"))
	s, err := loadState(state)
	if err != nil {
		t.Fatal(err)
	}
	if s.Version != 1 {
		t.Fatalf("new state version=%d want 1", s.Version)
	}
	if _, err = s.ingest(path, now, "site"); err != nil {
		t.Fatal(err)
	}
	if err := s.saveState(state); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadState(state)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Version != 1 || requests(loaded) != 1 || loaded.days()[0].Vhost != "site" {
		t.Fatalf("round trip lost data: %+v", loaded)
	}
	st, err := loaded.ingest(path, now, "site")
	if err != nil || st.Lines != 0 || requests(loaded) != 1 {
		t.Fatalf("round trip lost checkpoint: %+v %v", st, err)
	}
	for _, version := range []int{0, 2, 99} {
		s.Version = version
		before, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		writeTest(t, state, string(before))
		var output bytes.Buffer
		if err := Run(context.Background(), []string{"-state", state, "-output", filepath.Join(dir, "index.html"), path}, &output); err == nil || !strings.Contains(err.Error(), "unsupported state version") {
			t.Fatalf("version%d accepted: %v", version, err)
		}
		after, err := os.ReadFile(state)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("version%d file overwritten: %v", version, err)
		}
	}
}

func TestReferrersExternalOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log")
	now := time.Now()
	withRef := func(host, ref, p string) string {
		l := logLine(now, p)
		l = strings.Replace(l, `"https://example.org/?secret=yes"`, `"`+ref+`"`, 1)
		return host + " " + l
	}
	writeTest(t, path,
		withRef("site.example", "https://site.example/self", "/in")+
			withRef("site.example", "https://sub.site.example/self", "/in2")+
			withRef("site.example", "https://other.org/x", "/out")+
			withRef("site.example", "https://site.example.evil.com/x", "/out2")+
			withRef("site.example", "-", "/dash"))
	s, err := loadState(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ingest(path, now, "site.example"); err != nil {
		t.Fatal(err)
	}
	var d Day
	for _, day := range s.days() {
		d = day
	}
	if len(d.Referrers) != 2 {
		t.Fatalf("expected only external referrers, got %+v", d.Referrers)
	}
	if _, ok := d.Referrers["https://other.org/x"]; !ok {
		t.Fatal("external referrer missing")
	}
	if _, ok := d.Referrers["https://site.example.evil.com/x"]; !ok {
		t.Fatal("spoofed-looking external referrer missing")
	}
	if _, ok := d.Referrers["https://site.example/self"]; ok {
		t.Fatal("internal referrer must be excluded")
	}
}

func TestVhostAndSearchAggregation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log")
	now := time.Now()
	line := strings.Replace(logLine(now, "/landing?token=private"), "https://example.org/?secret=yes", "https://www.google.com/search?q=go+logs&token=private", 1)
	writeTest(t, path, "one.example "+line+"two.example "+line+line)
	s, err := loadState(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ingest(path, now, ""); err != nil {
		t.Fatal(err)
	}
	if len(s.days()) != 3 || requests(s) != 3 {
		t.Fatal("vhosts not separated")
	}
	for _, d := range s.days() {
		if d.Terms["go logs"] != 1 {
			t.Fatalf("missing search term: %+v", d)
		}
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("token=")) || bytes.Contains(b, []byte("private")) {
		t.Fatal("query secret persisted")
	}
}

func TestVhostMappingAndGeneratedOutputSafety(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log")
	state := filepath.Join(dir, "state")
	index := filepath.Join(dir, "index.html")
	now := time.Now()
	writeTest(t, path, logLine(now, "/mapped"))
	args := []string{"-state", state, "-output", index, "-vhost", "example.org=" + path}
	if err := Run(context.Background(), args, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	s, err := loadState(state)
	if err != nil {
		t.Fatal(err)
	}
	if s.days()[0].Vhost != "example.org" {
		t.Fatal("source mapping not used")
	}
	for _, page := range BuildPages(index, s.report(now, false)) {
		if _, err := os.Stat(page.Path); err != nil {
			t.Fatal("missing generated page", err)
		}
	}
	var mappings vhostFlags
	if err := mappings.Set("bad"); err == nil {
		t.Fatal("accepted bad vhost mapping")
	}
	if err := mappings.Set("a=" + path); err != nil {
		t.Fatal(err)
	}
	if err := mappings.Set("b=" + path); err != nil {
		t.Fatal(err)
	}
	if _, err := mappings.expand(); err == nil {
		t.Fatal("conflicting mappings accepted")
	}
	if sourceVhost(path) != sourceVhost(path+".1") {
		t.Fatal("rotated source has different vhost")
	}
	// A generated month page may itself be an input, not just the index.
	collision := filepath.Join(dir, "index-month-"+now.UTC().Format("2006-01")+".html")
	writeTest(t, collision, logLine(now, "/do-not-overwrite"))
	if err := Run(context.Background(), []string{"-state", state, "-output", index, collision}, &bytes.Buffer{}); err == nil {
		t.Fatal("allowed overwriting input with a detail page")
	}
	b, _ := os.ReadFile(collision)
	if !bytes.Contains(b, []byte("/do-not-overwrite")) {
		t.Fatal("input overwritten")
	}
}

func TestCountryVisitorsAggregation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log")
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	// Four distinct client IPs on one day: DE, FR, a private-range one, and an
	// unresolvable one. Countries counts requests; CountryVisitors counts
	// unique visitor hashes.
	lines := ""
	for i, host := range []string{"10.0.0.1", "10.0.0.2", "10.0.0.3", "10.0.0.4"} {
		l := strings.Replace(logLine(now, fmt.Sprintf("/p%d", i)), "127.0.0.1", host, 1)
		lines += l
	}
	writeTest(t, path, lines)
	s, err := loadState(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	s.geo = func(host string) (string, error) {
		switch host {
		case "10.0.0.1":
			return "DE", nil
		case "10.0.0.2":
			return "FR", nil
		case "10.0.0.3":
			return localPrivate, nil // private range must land in its own bucket
		}
		return "", nil // 10.0.0.4 must land in the Unknown bucket
	}
	if _, err := s.ingest(path, now, "site"); err != nil {
		t.Fatal(err)
	}
	d := s.days()[0]
	if d.Countries["DE"] != 1 || d.Countries["FR"] != 1 || d.Countries[localPrivate] != 1 || d.Countries["Unknown"] != 1 {
		t.Fatalf("Countries must stay request counts, buckets included: %v", d.Countries)
	}
	if len(d.CountryVisitors["DE"]) != 1 || len(d.CountryVisitors["FR"]) != 1 || len(d.CountryVisitors[localPrivate]) != 1 || len(d.CountryVisitors["Unknown"]) != 1 || len(d.CountryVisitors) != 4 {
		t.Fatalf("wrong per-country visitor sets: %v", d.CountryVisitors)
	}
	for id := range d.CountryVisitors["DE"] {
		if !d.Visitors[id] {
			t.Fatal("country visitor id missing from day visitors")
		}
	}
	// groupDays must union the sets across days without double-counting.
	other := d
	other.Date = "2026-11-04"
	merged := groupDays([]Day{d, other}, 7)
	if len(merged) != 1 || len(merged[0].CountryVisitors["DE"]) != 1 || len(merged[0].CountryVisitors["FR"]) != 1 {
		t.Fatalf("groupDays lost country visitors: %v", merged)
	}
	if merged[0].Countries["DE"] != 2 || merged[0].Countries["FR"] != 2 {
		t.Fatalf("groupDays must sum request counts: %v", merged[0].Countries)
	}
	// Old state has request counts but no country visitor sets.
	s.Months["2026-11"].Hosts["site"]["2026-11-03"].CountryVisitors = nil
	statePath := filepath.Join(dir, "state")
	if err := s.saveState(statePath); err != nil {
		t.Fatal(err)
	}
	legacy, err := loadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	legacy.geo = s.geo
	writeTest(t, path, lines+strings.Replace(logLine(now, "/new"), "127.0.0.1", "10.0.0.1", 1))
	if _, err := legacy.ingest(path, now, "site"); err != nil {
		t.Fatal(err)
	}
	updated := legacy.days()[0]
	if updated.Requests != 5 || updated.Countries["DE"] != 2 || len(updated.CountryVisitors["DE"]) != 1 {
		t.Fatalf("old-state append lost data: %+v", updated)
	}
}

func TestInvalidSessionStateRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")
	s, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Sessions["example.org"] = map[string][]Visit{"client": {{Start: 20, End: 10}}}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	writeTest(t, path, string(b))
	if _, err := loadState(path); err == nil {
		t.Fatal("accepted inverted session interval")
	}
}
