package reqwind

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInitialArchiveDiscovery(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "access.log")
	archive := filepath.Join(dir, "archive")
	if err := os.Mkdir(archive, 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	writeTest(t, base, logLine(now, "/active"))
	writeGzip(t, base+".2.gz", logLine(now.Add(-2*time.Hour), "/numbered"), false)
	writeGzip(t, filepath.Join(archive, "access.log-"+now.AddDate(0, 0, -1).Format("20060102")+".gz"), logLine(now.AddDate(0, 0, -1), "/date"), false)
	writeTest(t, base+".notes", "must not ingest\n")
	paths, err := discoverLogs([]string{base})
	if err != nil || len(paths) != 3 {
		t.Fatalf("discovery=%v err=%v", paths, err)
	}
	for _, p := range paths {
		if sourceVhost(p) != sourceVhost(base) {
			t.Fatalf("archive grouped separately: %s", p)
		}
	}
	previous := defaultLogBases
	defaultLogBases = []string{base}
	t.Cleanup(func() { defaultLogBases = previous })
	state, index := filepath.Join(dir, "state"), filepath.Join(dir, "index.html")
	args := []string{"-state", state, "-output", index, "-geo-db", "off"}
	var output bytes.Buffer
	if err := Run(context.Background(), args, &output); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), args, &output); err != nil {
		t.Fatal(err)
	}
	s, err := loadState(state)
	if err != nil || requests(s) != 3 {
		t.Fatalf("wrong archive totals: %v %+v", err, s)
	}
	if !strings.Contains(output.String(), "0 new lines") {
		t.Fatal(output.String())
	}
}

func TestFolderArgsSelectLogsAndZips(t *testing.T) {
	dir := t.TempDir()
	logs := filepath.Join(dir, "logs")
	if err := os.Mkdir(logs, 0700); err != nil {
		t.Fatal(err)
	}
	state, index := filepath.Join(dir, "state"), filepath.Join(dir, "index.html")
	now := time.Now()
	// A directory holding a plain log and a gzipped log.
	writeTest(t, filepath.Join(logs, "access.log"), logLine(now, "/plain"))
	writeGzip(t, filepath.Join(logs, "access.log.1.gz"), logLine(now.Add(-time.Hour), "/zipped"), false)
	writeTest(t, filepath.Join(logs, "error.log"), "not an access log\n")
	writeTest(t, filepath.Join(logs, "site-error.log.1"), logLine(now, "/skip-plain"))
	writeGzip(t, filepath.Join(logs, "site-error.log.2.gz"), logLine(now, "/skip-zipped"), false)
	var output bytes.Buffer
	if err := Run(context.Background(), []string{"-state", state, "-output", index, "-geo-db", "off", logs}, &output); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), []string{"-state", state, "-output", index, "-geo-db", "off", logs}, &output); err != nil {
		t.Fatal(err)
	}
	s, err := loadState(state)
	if err != nil {
		t.Fatal(err)
	}
	// Both the plain and zipped logs are ingested from the folder.
	if requests(s) != 2 {
		t.Fatalf("folder args should ingest 2 lines, got %d", requests(s))
	}
	if s.InvalidLines != 0 {
		t.Fatal("folder ingestion read an error log")
	}
	// A second run re-ingests zero (checkpoints are stable).
	if !strings.Contains(output.String(), "0 new lines") {
		t.Fatal(output.String())
	}
	// A folder with no logs is an error.
	empty := t.TempDir()
	err = Run(context.Background(), []string{"-state", filepath.Join(empty, "state"), "-output", filepath.Join(empty, "index.html"), "-geo-db", "off", empty}, &output)
	if err == nil {
		t.Fatal("an empty folder should report no matching logs")
	}
}

func TestVerboseAndPaletteFlags(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log")
	state, index := filepath.Join(dir, "state"), filepath.Join(dir, "index.html")
	writeTest(t, path, logLine(time.Now(), "/"))
	var output bytes.Buffer
	if err := Run(context.Background(), []string{"-verbose", "-palette", "forest", "-geo-db", "off", "-state", state, "-output", index, path}, &output); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"reading", "checkpoints", "GeoIP: disabled", "new decoded bytes", "reqwind: wrote " + index, "reqwind: wrote " + state} {
		if !strings.Contains(output.String(), text) {
			t.Fatalf("missing verbose %s: %s", text, output.String())
		}
	}
	b, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte(`data-palette="forest"`)) {
		t.Fatal("palette flag not rendered")
	}
	if err := Run(context.Background(), []string{"-palette", "bad"}, &output); err == nil {
		t.Fatal("accepted invalid palette")
	}
}

func TestVerboseInvalidLines(t *testing.T) {
	for _, verbose := range []bool{false, true} {
		t.Run(fmt.Sprint(verbose), func(t *testing.T) {
			dir := t.TempDir()
			plain, archive := filepath.Join(dir, "a.log"), filepath.Join(dir, "b.log.gz")
			bad := "invalid\x1b[31m\rline\n"
			writeTest(t, plain, logLine(time.Now(), "/")+strings.Repeat(bad, 6))
			writeGzip(t, archive, strings.Repeat("archive invalid\n", 6), false)
			args := []string{"-geo-db", "off", "-state", filepath.Join(dir, "state"), "-output", filepath.Join(dir, "index.html")}
			if verbose {
				args = append(args, "-verbose")
			}
			args = append(args, plain, archive)
			var output bytes.Buffer
			if err := Run(context.Background(), args, &output); err != nil {
				t.Fatal(err)
			}
			want := 0
			if verbose {
				want = 10
			}
			if got := strings.Count(output.String(), "invalid line at decoded byte"); got != want {
				t.Fatalf("printed %d invalid lines, want %d: %s", got, want, &output)
			}
			if verbose && (!strings.Contains(output.String(), `invalid\x1b[31m\rline`) || strings.Count(output.String(), `"archive invalid"`) != 4) {
				t.Fatalf("invalid lines were not escaped or limited across files: %s", &output)
			}
			if strings.ContainsAny(output.String(), "\x1b\r") {
				t.Fatal("raw control characters in output")
			}
			output.Reset()
			if err := Run(context.Background(), args, &output); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(output.String(), "invalid line at decoded byte") {
				t.Fatal("printed invalid lines already read")
			}
		})
	}
}
