package reqwind

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestWriteReportsHonorsCancellation guards the slow phase: with the context
// already cancelled, writeReports must stop at the first page boundary and
// must not commit state or write any page. Without this the render loop
// ignored the signal and Ctrl+C appeared to do nothing during generation.
func TestWriteReportsHonorsCancellation(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	index := filepath.Join(dir, "index.html")
	log := filepath.Join(dir, "log")
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	writeTest(t, log, logLine(now, "/new"))
	s, err := loadState(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ingest(log, now, "example.org"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled: the very first boundary must trip
	s.ctx = ctx
	err = writeReports(s, state, index, BuildPages(index, s.report(now, false)), []string{log}, &bytes.Buffer{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if _, err := os.Stat(index); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("index page must not be written after cancellation")
	}
	if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("state must not be committed after cancellation")
	}
}

// An unchanged rerun (no new log lines, same version) must not rewrite the
// published pages or touch their mtimes; a changed version rebuilds all of
// them.
func TestUnchangedRerunKeepsPublishedFiles(t *testing.T) {
	dir := t.TempDir()
	state, index := filepath.Join(dir, "state"), filepath.Join(dir, "index.html")
	log := filepath.Join(dir, "log")
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	writeTest(t, log, "one.example "+logLine(now, "/new")+"two.example "+logLine(now, "/other"))
	s, err := loadState(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ingest(log, now, ""); err != nil {
		t.Fatal(err)
	}
	entries := func() map[string]time.Time {
		names, err := filepath.Glob(filepath.Join(dir, "*.html"))
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, state, state+"-2026-03.json")
		entries := map[string]time.Time{}
		for _, name := range names {
			info, err := os.Stat(name)
			if err != nil {
				t.Fatal(err)
			}
			entries[name] = info.ModTime()
		}
		return entries
	}
	if err := writeReports(s, state, index, BuildPages(index, s.report(now, false)), []string{log}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	previous := entries()
	if len(previous) == 0 {
		t.Fatal("no pages written")
	}
	// A second run with a later clock reads zero new lines; with the same
	// version every page must keep its stored bytes and mtime.
	s2, err := loadState(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s2.ingest(log, now.Add(time.Hour), ""); err != nil {
		t.Fatal(err)
	}
	if err := writeReports(s2, state, index, BuildPages(index, s2.report(now.Add(time.Hour), false)), []string{log}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for path, before := range previous {
		after := entries()[path]
		if !after.Equal(before) {
			t.Fatalf("unchanged file rewritten: %s", path)
		}
	}
	// A new reqwind version rebuilds every page.
	version := Version
	Version = "9.9.9"
	t.Cleanup(func() { Version = version })
	s3, err := loadState(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeReports(s3, state, index, BuildPages(index, s3.report(now.Add(2*time.Hour), false)), []string{log}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for path, before := range previous {
		if filepath.Ext(path) != ".html" {
			continue
		}
		after := entries()[path]
		if after.Equal(before) {
			t.Fatalf("page not rebuilt after version change: %s", path)
		}
	}
}

func TestExpiredPagesAndModifiedFiles(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	index := filepath.Join(dir, "index.html")
	log := filepath.Join(dir, "log")
	now := time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)
	writeTest(t, log, logLine(time.Date(2024, 4, 1, 0, 0, 0, 0, time.UTC), "/old")+logLine(now, "/new"))
	s, err := loadState(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ingest(log, now, "example.org"); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := writeReports(s, state, index, BuildPages(index, s.report(now, false)), []string{log}, &output); err != nil {
		t.Fatal(err)
	}
	oldMonth := filepath.Join(dir, "index-month-2024-04.html")
	oldYear := filepath.Join(dir, "index-year-2024.html")
	original, err := os.ReadFile(oldMonth)
	if err != nil {
		t.Fatal(err)
	}
	// The user modifies one generated page and a symlink stands in for
	// another.
	writeTest(t, oldYear, "user modified page")
	hostPage := filepath.Join(dir, "changed-symlink.html")
	preservedFile := filepath.Join(dir, "saved-user-copy")
	writeTest(t, preservedFile, string(original))
	if err := os.Symlink(preservedFile, hostPage); err != nil {
		t.Fatal(err)
	}
	s.Outputs[index][hostPage] = s.Outputs[index][oldMonth]
	// Retention is manual: the user removes the 2024-04 month by deleting
	// its month bucket file, so its pages are no longer regenerated and
	// become expired content.
	delete(s.Months, "2024-04")
	if err := os.Remove(filepath.Join(dir, "state-2024-04.json")); err != nil {
		t.Fatal(err)
	}
	now = time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	if err := writeReports(s, state, index, BuildPages(index, s.report(now, false)), []string{log}, &output); err != nil {
		t.Fatal(err)
	}
	// Expired pages are never deleted; the unchanged one keeps its bytes.
	kept, err := os.ReadFile(oldMonth)
	if err != nil {
		t.Fatalf("expired generated month deleted: %v", err)
	}
	if string(kept) != string(original) {
		t.Fatal("expired generated month content changed")
	}
	b, err := os.ReadFile(oldYear)
	if err != nil || string(b) != "user modified page" {
		t.Fatal("modified page destroyed")
	}
	if !bytes.Contains(output.Bytes(), []byte("keeping modified expired page")) {
		t.Fatal("missing modified-page warning")
	}
	if info, err := os.Lstat(hostPage); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("changed symlink removed", err)
	}
	// Retention is manual: the untouched 2026-03 month is still persisted.
	persisted, err := loadState(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := persisted.Months["2026-03"]; !ok {
		t.Fatal("untouched month dropped")
	}
	if _, ok := persisted.Months["2024-04"]; ok {
		t.Fatal("manually removed month came back")
	}
	if _, ok := persisted.Outputs[index][oldMonth]; !ok {
		t.Fatal("expired page missing from manifest")
	}
}
