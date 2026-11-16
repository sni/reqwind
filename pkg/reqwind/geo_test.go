package reqwind

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeTool writes a shell script named name into dir that behaves per body.
func fakeTool(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func mustDB(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "fake.mmdb")
	if err := os.WriteFile(p, []byte("not a real mmdb, only the tool is faked"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// fakeMMDB installs a fake mmdblookup in tmp matching $4 (--file DB --ip IP ...):
// Known IPs print an iso code; unknown and broken database cases use distinct errors.
// anything else sleeps (for timeout tests).
func fakeMMDB(t *testing.T, tmp string) string {
	db := mustDB(t, tmp)
	fakeTool(t, tmp, "mmdblookup", `
case "$4" in
  8.8.8.8) printf '\n  "US" <utf8_string>\n'; exit 0 ;;
  9.9.9.9|7.7.7.7) printf '\n  "DE" <utf8_string>\n'; exit 0 ;;
  5.5.5.5) printf 'Could not find an entry for this IP address (5.5.5.5)\n' 1>&2; exit 6 ;;
  4.4.4.4) printf 'No data was found at the lookup path you provided\n' 1>&2; exit 5 ;;
  6.6.6.6) printf 'bogus db header\n' 1>&2; exit 2 ;;
  *) sleep 10; exit 0 ;;
esac
`)
	return db
}

func TestLoadGeo(t *testing.T) {
	t.Run("off", func(t *testing.T) {
		lk, desc, err := loadGeo(context.Background(), "off")
		if err != nil || lk != nil || desc != "disabled" {
			t.Fatalf("got %v %q %v", lk, desc, err)
		}
	})
	t.Run("auto no db", func(t *testing.T) {
		orig := geoCandidates
		geoCandidates = []string{filepath.Join(t.TempDir(), "nope.mmdb")}
		defer func() { geoCandidates = orig }()
		lk, desc, err := loadGeo(context.Background(), "")
		if err != nil || lk != nil || desc == "" {
			t.Fatalf("got %v %q %v", lk, desc, err)
		}
	})
	t.Run("auto discovers db", func(t *testing.T) {
		tmp := t.TempDir()
		db := fakeMMDB(t, tmp)
		orig := geoCandidates
		geoCandidates = []string{db}
		defer func() { geoCandidates = orig }()
		old := os.Getenv("PATH")
		os.Setenv("PATH", tmp+string(os.PathListSeparator)+old)
		defer os.Setenv("PATH", old)
		lk, desc, err := loadGeo(context.Background(), "")
		if err != nil || lk == nil || desc != db {
			t.Fatalf("got %v %q %v", lk, desc, err)
		}
		if c, err := lk("8.8.8.8"); err != nil || c != "US" {
			t.Fatalf("got %q %v", c, err)
		}
	})
	t.Run("explicit missing", func(t *testing.T) {
		_, _, err := loadGeo(context.Background(), filepath.Join(t.TempDir(), "missing.mmdb"))
		if err == nil || !strings.Contains(err.Error(), "missing") {
			t.Fatalf("want missing error, got %v", err)
		}
	})
	t.Run("explicit no tool", func(t *testing.T) {
		tmp := t.TempDir()
		mustDB(t, tmp)
		old := os.Getenv("PATH")
		os.Setenv("PATH", t.TempDir()) // no mmdblookup / geoiplookup
		defer os.Setenv("PATH", old)
		_, _, err := loadGeo(context.Background(), filepath.Join(tmp, "fake.mmdb"))
		if err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("want tool-not-found error, got %v", err)
		}
	})
}

func TestLookup(t *testing.T) {
	tmp := t.TempDir()
	db := fakeMMDB(t, tmp)
	old := os.Getenv("PATH")
	os.Setenv("PATH", tmp+string(os.PathListSeparator)+old)
	t.Cleanup(func() { os.Setenv("PATH", old) })
	r, err := newGeoResolver(db)
	if err != nil {
		t.Fatal(err)
	}
	lk := r.lookup

	cases := map[string]string{
		"8.8.8.8": "US", "9.9.9.9": "DE", "7.7.7.7": "DE", "5.5.5.5": "", "4.4.4.4": "", "::ffff:8.8.8.8": "US",
		"127.0.0.1": localPrivate, "10.0.0.1": localPrivate, "192.168.1.1": localPrivate,
		"169.254.1.1": localPrivate, "0.0.0.0": localPrivate,
		"::1": localPrivate, "fe80::1": localPrivate, "fd00::1": localPrivate, "ff02::1": localPrivate,
		"::ffff:127.0.0.1": localPrivate,
		"hostname":         "", "not-an-ip": "",
	}
	for ip, want := range cases {
		got, err := lk(ip)
		if err != nil || got != want {
			t.Errorf("%s: got %q %v, want %q", ip, got, err, want)
		}
	}
	if _, err := lk("6.6.6.6"); err == nil || !strings.Contains(err.Error(), "bogus db header") {
		t.Errorf("real tool error must surface, got %v", err)
	}
}

func TestAutoGeoReportsMissingLegacyTool(t *testing.T) {
	tmp := t.TempDir()
	db := filepath.Join(tmp, "GeoIP.dat")
	writeTest(t, db, "fixture database")
	previous := geoCandidates
	geoCandidates = []string{filepath.Join(tmp, "missing.mmdb"), db}
	t.Cleanup(func() { geoCandidates = previous })
	t.Setenv("PATH", tmp)
	fakeTool(t, tmp, "mmdblookup", `echo '"DE" <utf8_string>'`)
	lookup, description, err := loadGeo(context.Background(), "")
	if err != nil || lookup != nil || !strings.Contains(description, db) || !strings.Contains(description, "geoiplookup not found") {
		t.Fatalf("missing useful detection error: lookup=%v description=%q err=%v", lookup, description, err)
	}
	fakeTool(t, tmp, "geoiplookup", `echo 'GeoIP Country Edition: DE, Germany'`)
	lookup, _, err = loadGeo(context.Background(), "")
	if err != nil || lookup == nil {
		t.Fatalf("legacy database not detected: %v", err)
	}
	if country, err := lookup("8.8.8.8"); err != nil || country != "DE" {
		t.Fatalf("legacy lookup=%q err=%v", country, err)
	}
}

func TestLookupCache(t *testing.T) {
	tmp := t.TempDir()
	calls := filepath.Join(tmp, "calls")
	db := mustDB(t, tmp)
	fakeTool(t, tmp, "mmdblookup", `
case "$4" in
  8.8.8.8) echo US ;;
  1.1.1.1) echo FR ;;
esac
printf '%s\n' "$4" >> `+calls)
	old := os.Getenv("PATH")
	os.Setenv("PATH", tmp+string(os.PathListSeparator)+old)
	t.Cleanup(func() { os.Setenv("PATH", old) })
	r, err := newGeoResolver(db)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if c, err := r.lookup("8.8.8.8"); err != nil || c != "US" {
			t.Fatalf("got %q %v", c, err)
		}
	}
	if b, _ := os.ReadFile(calls); len(strings.Split(strings.TrimSpace(string(b)), "\n")) != 1 {
		t.Errorf("cache miss: tool called %d times, want 1", len(strings.Split(strings.TrimSpace(string(b)), "\n")))
	}
	if c, err := r.lookup("1.1.1.1"); err != nil || c != "FR" {
		t.Errorf("got %q %v", c, err)
	}
}

func TestLookupCap(t *testing.T) {
	orig := geoCacheCap
	geoCacheCap = 2
	defer func() { geoCacheCap = orig }()
	tmp := t.TempDir()
	db := mustDB(t, tmp)
	fakeTool(t, tmp, "mmdblookup", `echo XX`)
	old := os.Getenv("PATH")
	os.Setenv("PATH", tmp+string(os.PathListSeparator)+old)
	t.Cleanup(func() { os.Setenv("PATH", old) })
	r, err := newGeoResolver(db)
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := r.lookup("1.1.1.1"); c != "XX" {
		t.Fatalf("got %q", c)
	}
	if c, _ := r.lookup("2.2.2.2"); c != "XX" {
		t.Fatalf("got %q", c)
	}
	if c, _ := r.lookup("3.3.3.3"); c != "" {
		t.Fatalf("over cap should return empty, got %q", c)
	}
	if c, _ := r.lookup("1.1.1.1"); c != "XX" {
		t.Fatalf("cached entry lost, got %q", c)
	}
}

func TestLookupTimeoutAndCancel(t *testing.T) {
	tmp := t.TempDir()
	db := mustDB(t, tmp)
	// exec replaces the shell so CommandContext kills the process directly;
	// a shell child holding the pipe would make Output() wait for it.
	fakeTool(t, tmp, "mmdblookup", `exec sleep 10`)
	old := os.Getenv("PATH")
	os.Setenv("PATH", tmp+string(os.PathListSeparator)+old)
	t.Cleanup(func() { os.Setenv("PATH", old) })
	r, err := newGeoResolver(db)
	if err != nil {
		t.Fatal(err)
	}
	orig := geoTimeout
	geoTimeout = 150 * time.Millisecond
	defer func() { geoTimeout = orig }()
	start := time.Now()
	if _, err := r.lookup("1.2.3.4"); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("want timeout error, got %v", err)
	}
	if d := time.Since(start); d < 100*time.Millisecond || d > 2*time.Second {
		t.Fatalf("timeout took %v", d)
	}
	// loadGeo reports a canceled load context instead of running.
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := loadGeo(cctx, "off"); err == nil {
		t.Fatal("canceled context must be reported at load time")
	}
}

func TestDotDAT(t *testing.T) {
	tmp := t.TempDir()
	db := filepath.Join(tmp, "GeoIP.dat")
	if err := os.WriteFile(db, []byte("legacy db"), 0o644); err != nil {
		t.Fatal(err)
	}
	fakeTool(t, tmp, "geoiplookup", `echo "GeoIP Country Edition: US, United States"`)
	old := os.Getenv("PATH")
	os.Setenv("PATH", tmp+string(os.PathListSeparator)+old)
	t.Cleanup(func() { os.Setenv("PATH", old) })
	r, err := newGeoResolver(db)
	if err != nil {
		t.Fatal(err)
	}
	if c, err := r.lookup("8.8.8.8"); err != nil || c != "US" {
		t.Fatalf("got %q %v", c, err)
	}
}

func TestGeoFormatsAndRuntimeCancellation(t *testing.T) {
	for _, tc := range []struct {
		text   string
		legacy bool
		want   string
	}{
		{"\n  \"GB\" <utf8_string>\n", false, "GB"},
		{"GeoIP Country Edition: DE, Germany", true, "DE"},
		{"GeoIP Country Edition: IP Address not found", true, ""},
		{"\"bogus\" <utf8_string>", false, ""},
		{"\"US\" garbage", false, ""},
	} {
		if got := countryCode(tc.text, tc.legacy); got != tc.want {
			t.Fatalf("%q: %q want %q", tc.text, got, tc.want)
		}
	}
	tmp := t.TempDir()
	db := fakeMMDB(t, tmp)
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithCancel(context.Background())
	lookup, _, err := loadGeo(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := lookup("8.8.8.8"); !errors.Is(err, context.Canceled) {
		t.Fatalf("lookup ignored parent cancellation: %v", err)
	}
}
