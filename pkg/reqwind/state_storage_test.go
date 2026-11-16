package reqwind

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestStateCompressionAndLegacyMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")
	s, err := freshState()
	if err != nil {
		t.Fatal(err)
	}
	d := s.ensureDay("2026-01-01", "example.org")
	d.Requests = 1
	d.Paths["/page"] = 1
	s.Sessions["example.org"] = map[string][]Visit{"client": {{Start: 10, End: 20}}}
	for file, value := range map[string]any{path: s, path + "-2026-01.json": s.Months["2026-01"]} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		writeTest(t, file, string(data))
	}
	loaded, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := loaded.saveState(path); err != nil {
		t.Fatal(err)
	}
	stored := map[string][]byte{}
	old := time.Unix(1000000000, 0)
	for _, file := range []string{path, path + "-2026-01.json"} {
		data, err := os.ReadFile(file)
		if err != nil || !bytes.HasPrefix(data, []byte(gzipMagic)) {
			t.Fatalf("not compressed: %s: %v", file, err)
		}
		stored[file] = data
		if err := os.Chtimes(file, old, old); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err = loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.Months, loaded.Months) || !reflect.DeepEqual(s.Sessions, loaded.Sessions) || s.Salt != loaded.Salt {
		t.Fatal("migration changed state data")
	}
	if err := loaded.saveState(path); err != nil {
		t.Fatal(err)
	}
	for file, before := range stored {
		after, err := os.ReadFile(file)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("unchanged state bytes changed: %s: %v", file, err)
		}
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		if !info.ModTime().Equal(old) {
			t.Fatalf("unchanged state rewritten: %s", file)
		}
	}
}

func TestCorruptCompressedStateRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")
	s, err := freshState()
	if err != nil {
		t.Fatal(err)
	}
	s.ensureDay("2026-01-01", "example.org")
	if err := s.saveState(path); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{path, path + "-2026-01.json"} {
		good, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		checksum := bytes.Clone(good)
		checksum[len(checksum)-8] ^= 0xff
		for _, bad := range [][]byte{good[:3], good[:len(good)-1], checksum, append(bytes.Clone(good), 'x')} {
			if err := os.WriteFile(file, bad, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadState(path); err == nil {
				t.Fatalf("accepted corrupt state: %s", file)
			}
			after, err := os.ReadFile(file)
			if err != nil || !bytes.Equal(after, bad) {
				t.Fatalf("corrupt state changed: %v", err)
			}
		}
		if err := os.WriteFile(file, good, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func benchmarkMonth(b *testing.B) *Month {
	b.Helper()
	s, err := freshState()
	if err != nil {
		b.Fatal(err)
	}
	for day := 1; day <= 7; day++ {
		d := s.ensureDay(fmt.Sprintf("2026-01-%02d", day), "example.org")
		d.PathBytes = map[string]int64{}
		for i := 0; i < 1000; i++ {
			id := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprint(i))))[:32]
			path := fmt.Sprintf("/downloads/package-%04d/archive.tar.gz", i)
			d.Paths[path] = int64(i + 1)
			d.PathBytes[path] = int64(i+1) * 4096
			d.Visitors[id] = true
			d.Clients[id] = ClientStats{Requests: int64(i + 1), Bytes: 4096, Raw: fmt.Sprintf("192.0.%d.%d", i/256, i%256)}
			d.Requests += int64(i + 1)
		}
	}
	return s.Months["2026-01"]
}

func BenchmarkStateJSON(b *testing.B) {
	m := benchmarkMonth(b)
	data, err := json.Marshal(m)
	if err != nil {
		b.Fatal(err)
	}
	b.Run("Marshal", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := json.Marshal(m); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Unmarshal", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(data)))
		for i := 0; i < b.N; i++ {
			var m Month
			if err := json.Unmarshal(data, &m); err != nil {
				b.Fatal(err)
			}
		}
	})
	for _, level := range []int{gzip.BestSpeed, gzip.DefaultCompression} {
		b.Run(fmt.Sprintf("Gzip/%d", level), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			for i := 0; i < b.N; i++ {
				var buf bytes.Buffer
				w, err := gzip.NewWriterLevel(&buf, level)
				if err != nil {
					b.Fatal(err)
				}
				if _, err := w.Write(data); err != nil {
					b.Fatal(err)
				}
				if err := w.Close(); err != nil {
					b.Fatal(err)
				}
				b.ReportMetric(float64(buf.Len()), "stored-B")
				b.ReportMetric(float64(buf.Len())/float64(len(data))*100, "size-%")
			}
		})
	}
}
