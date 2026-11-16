package reqwind

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeGzip(t *testing.T, path, text string, appendMember bool) {
	t.Helper()
	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if appendMember {
		flags = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}
	f, err := os.OpenFile(path, flags, 0600)
	if err != nil {
		t.Fatal(err)
	}
	z := gzip.NewWriter(f)
	if _, err := z.Write([]byte(text)); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestGzipCheckpointsAndMembers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.gz")
	now := time.Now()
	first := strings.TrimSuffix(logLine(now, "/one"), "\n")
	writeGzip(t, path, first, false)
	s, err := loadState(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	scan := func(want, newLines int64) {
		t.Helper()
		st, err := s.ingest(path, now, "site")
		if err != nil || requests(s) != want || st.Lines != newLines {
			t.Fatalf("requests=%d stats=%+v err=%v", requests(s), st, err)
		}
	}
	scan(1, 1)
	scan(1, 0)
	writeGzip(t, path, "\n"+logLine(now, "/two"), true)
	// The newline terminates a previously complete archive line, not another request.
	scan(2, 2)
	renamed := filepath.Join(dir, "log.2.gz")
	if err := os.Rename(path, renamed); err != nil {
		t.Fatal(err)
	}
	path = renamed
	scan(2, 0)
	replacement := path + ".new"
	writeGzip(t, replacement, first+"\n"+logLine(now, "/two"), false)
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	scan(2, 0)
}

func TestCompressionRotationDoesNotRecount(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access.log")
	rotated := path + ".1"
	now := time.Now()
	text := logLine(now, "/first") + logLine(now, "/second")
	writeTest(t, path, text)
	s, err := loadState(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	scan := func(p string, want int64) {
		t.Helper()
		if _, err := s.ingest(p, now, sourceVhost(p)); err != nil {
			t.Fatal(err)
		}
		if requests(s) != want {
			t.Fatalf("%s requests=%d want%d", p, requests(s), want)
		}
	}
	scan(path, 2)
	if err := os.Rename(path, rotated); err != nil {
		t.Fatal(err)
	}
	scan(rotated, 2)
	writeGzip(t, rotated+".gz", text, false)
	scan(rotated+".gz", 2)
	writeTest(t, path, logLine(now, "/third"))
	scan(path, 3)
	scan(rotated+".gz", 3)
}

func TestCorruptGzipDoesNotCommit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "good.log")
	bad := filepath.Join(dir, "bad.gz")
	state := filepath.Join(dir, "state")
	index := filepath.Join(dir, "index.html")
	writeTest(t, path, logLine(time.Now(), "/good"))
	if err := Run(context.Background(), []string{"-geo-db", "off", "-state", state, "-output", index, path}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	writeGzip(t, bad, logLine(time.Now(), "/bad"), false)
	b, err := os.ReadFile(bad)
	if err != nil {
		t.Fatal(err)
	}
	b[len(b)-8] ^= 0xff
	if err := os.WriteFile(bad, b, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), []string{"-geo-db", "off", "-state", state, "-output", index, path, bad}, &bytes.Buffer{}); err == nil {
		t.Fatal("accepted bad gzip checksum")
	}
	after, _ := os.ReadFile(state)
	if !bytes.Equal(before, after) {
		t.Fatal("committed partially ingested corrupt archive")
	}
}

func TestLogicalFingerprintSamples(t *testing.T) {
	for _, size := range []int{0, 1, 255, 256, 257, 511, 512, 513, 4097} {
		data := []byte(strings.Repeat("0123456789", size/10+1)[:size])
		for _, chunk := range []int{1, 127, 256, 777} {
			var samples logSamples
			for i := 0; i < len(data); i += chunk {
				samples.Write(data[i:min(len(data), i+chunk)])
			}
			head, tail := samples.sums()
			a, b := sha256.Sum256(data[:min(len(data), 256)]), sha256.Sum256(data[max(0, len(data)-256):])
			if head != hex.EncodeToString(a[:]) || tail != hex.EncodeToString(b[:]) {
				t.Fatalf("size%d chunk%d: wrong samples", size, chunk)
			}
		}
	}
}

func TestGeoAggregationUsesNoRawClients(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log")
	now := time.Now()
	writeTest(t, path, logLine(now, "/"))
	s, err := loadState(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	s.hashClientAddresses()
	s.geo = func(host string) (string, error) {
		if host != "127.0.0.1" {
			t.Fatal(host)
		}
		return "DE", nil
	}
	if _, err := s.ingest(path, now, "site"); err != nil {
		t.Fatal(err)
	}
	d := s.days()[0]
	if d.Countries["DE"] != 1 || len(d.Clients) != 1 {
		t.Fatalf("missing geo/client metrics: %+v", d)
	}
	for id, client := range d.Clients {
		if len(id) != 32 || client.Raw != "" || client.Requests != 1 || client.Bytes != 42 {
			t.Fatalf("bad client metrics: %s %+v", id, client)
		}
	}
}

func TestCheckpointCandidatesFollowRotation(t *testing.T) {
	s := &State{Files: map[string]checkpoint{
		"same":       {Source: "site", Path: "access.log", Offset: 10},
		"other-path": {Source: "site", Path: "access.log.1", Offset: 20},
		"archive":    {Source: "site", Path: "access.log.2.gz", Compressed: true, Offset: 30},
		"other-site": {Source: "other", Path: "access.log", Offset: 40},
	}}
	check := func(want ...int64) {
		t.Helper()
		got := s.checkpointCandidates("new", "site", "access.log", false)
		if len(got) != len(want) {
			t.Fatalf("candidates = %+v, want offsets %v", got, want)
		}
		for i, cp := range got {
			if cp.Offset != want[i] {
				t.Fatalf("candidates = %+v, want offsets %v", got, want)
			}
		}
	}
	check(30, 10)
	s.setCheckpoint("same", checkpoint{Source: "site", Path: "access.log.3", Offset: 10})
	s.setCheckpoint("archive", checkpoint{Source: "other", Path: "access.log.2.gz", Compressed: true, Offset: 30})
	check()
	s.setCheckpoint("new-archive", checkpoint{Source: "site", Path: "access.log.4.gz", Compressed: true, Offset: 50})
	check(50)
}

func BenchmarkCheckpointCandidates(b *testing.B) {
	for _, n := range []int{1, 10000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			s := &State{Files: map[string]checkpoint{}}
			for i := 0; i < n; i++ {
				path := fmt.Sprintf("access.log.%d", i)
				s.Files[path] = checkpoint{Source: "site", Path: path, Offset: 1}
			}
			s.checkpointCandidates("new", "site", "access.log", false)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if got := s.checkpointCandidates("new", "site", "access.log", false); len(got) != 0 {
					b.Fatal("unrelated rotations were selected")
				}
			}
		})
	}
}

func BenchmarkIngestUnchangedCheckpoints(b *testing.B) {
	for _, n := range []int{1, 10000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			dir := b.TempDir()
			path := filepath.Join(dir, "access.log")
			now := time.Now()
			if err := os.WriteFile(path, []byte(logLine(now, "/")), 0600); err != nil {
				b.Fatal(err)
			}
			s, err := loadState(filepath.Join(dir, "state"))
			if err != nil {
				b.Fatal(err)
			}
			if _, err := s.ingest(path, now, "site"); err != nil {
				b.Fatal(err)
			}
			for i := 0; i < n; i++ {
				s.Files[fmt.Sprintf("old:%d", i)] = checkpoint{Source: "site", Compressed: true, Offset: 1}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := s.ingest(path, now, "site"); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
