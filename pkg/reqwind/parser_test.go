package reqwind

import (
	"strings"
	"testing"
	"time"
)

const combinedLine = `203.0.113.9 - - [02/Jan/2006:15:04:05 -0700] "GET /index.html?a=1&token=secret HTTP/1.1" 200 1234 "https://example.com/x" "curl/8.0"`

func TestCombined(t *testing.T) {
	e, err := ParseLine(combinedLine)
	if err != nil {
		t.Fatal(err)
	}
	wantTime, _ := time.Parse(timeLayout, "02/Jan/2006:15:04:05 -0700")
	if !e.Time.Equal(wantTime) {
		t.Errorf("time = %v", e.Time)
	}
	if e.Host != "203.0.113.9" {
		t.Errorf("host = %q", e.Host)
	}
	if e.Method != "GET" || e.Path != "/index.html" || e.Protocol != "HTTP/1.1" {
		t.Errorf("request = %q %q %q", e.Method, e.Path, e.Protocol)
	}
	if e.Status != 200 || e.Bytes != 1234 {
		t.Errorf("status/bytes = %d %d", e.Status, e.Bytes)
	}
	if e.Referer != "https://example.com/x" || e.Agent != "curl/8.0" {
		t.Errorf("referer/agent = %q %q", e.Referer, e.Agent)
	}
	if e.Vhost != "" {
		t.Errorf("vhost = %q, want empty", e.Vhost)
	}
}

func TestVhostPrefix(t *testing.T) {
	cases := []struct {
		line  string
		vhost string
		host  string
	}{
		{`[example.org] 203.0.113.9 - - [02/Jan/2006:15:04:05 -0700] "GET / HTTP/1.1" 200 1`, "example.org", "203.0.113.9"},
		{`example.org:443 203.0.113.9 - - [02/Jan/2006:15:04:05 -0700] "GET / HTTP/1.1" 200 1`, "example.org", "203.0.113.9"},
		{`[api.example.org:8443] 203.0.113.9 - - [02/Jan/2006:15:04:05 -0700] "GET / HTTP/1.1" 200 1`, "api.example.org", "203.0.113.9"},
		{`[2001:db8::100] 2001:db8::1 - - [02/Jan/2006:15:04:05 -0700] "GET / HTTP/1.1" 200 1`, "2001:db8::100", "2001:db8::1"},
		{`[2001:db8::100]:443 2001:db8::1 - - [02/Jan/2006:15:04:05 -0700] "GET / HTTP/1.1" 200 1`, "2001:db8::100", "2001:db8::1"},
		{`10.0.0.2 - - [02/Jan/2006:15:04:05 -0700] "GET / HTTP/1.1" 200 1`, "", "10.0.0.2"},
	}
	for _, c := range cases {
		e, err := ParseLine(c.line)
		if err != nil {
			t.Errorf("%s: %v", c.line, err)
			continue
		}
		if e.Vhost != c.vhost || e.Host != c.host {
			t.Errorf("%s => vhost %q host %q", c.line, e.Vhost, e.Host)
		}
	}
}

func TestCommonFormat(t *testing.T) {
	e, err := ParseLine(`192.168.0.1 - - [13/Nov/2026:08:00:00 +0000] "POST /api/x?q=z HTTP/1.0" 404 0`)
	if err != nil {
		t.Fatal(err)
	}
	if e.Method != "POST" || e.Path != "/api/x" || e.Protocol != "HTTP/1.0" || e.Status != 404 || e.Bytes != 0 {
		t.Errorf("got %+v", e)
	}
	if e.Referer != "" || e.Agent != "" {
		t.Errorf("expected empty referer/agent, got %q %q", e.Referer, e.Agent)
	}
}

func TestVariations(t *testing.T) {
	cases := []struct {
		line     string
		host     string
		method   string
		path     string
		protocol string
		status   int
		bytes    int64
	}{
		{`[logs.example.com] 10.0.0.2 - - [02/Jan/2006:15:04:05 -0700] "GET / HTTP/1.1" 301 10`, "10.0.0.2", "GET", "/", "HTTP/1.1", 301, 10},
		{`2001:db8::1 - - [02/Jan/2006:15:04:05 -0700] "GET / HTTP/1.1" 200 5`, "2001:db8::1", "GET", "/", "HTTP/1.1", 200, 5},
		{`10.0.0.3 - alice [02/Jan/2006:15:04:05 -0700] "GET /a HTTP/1.1" 200 -`, "10.0.0.3", "GET", "/a", "HTTP/1.1", 200, 0},
		{`10.0.0.4 - - [02/Jan/2006:15:04:05 -0700] "-" 408 -`, "10.0.0.4", "", "", "", 408, 0},
		{`10.0.0.5 - - [02/Jan/2006:15:04:05 -0700] "DELETE /p#frag HTTP/2" 599 999999999 "r" "a\"b"`, "10.0.0.5", "DELETE", "/p", "HTTP/2", 599, 999999999},
	}
	for _, c := range cases {
		e, err := ParseLine(c.line)
		if err != nil {
			t.Errorf("%s: %v", c.line, err)
			continue
		}
		if e.Host != c.host || e.Method != c.method || e.Path != c.path || e.Protocol != c.protocol || e.Status != c.status || e.Bytes != c.bytes {
			t.Errorf("%s => %+v", c.line, e)
		}
	}
}

func TestEscapes(t *testing.T) {
	e, err := ParseLine(`10.0.0.1 - - [02/Jan/2006:15:04:05 -0700] "GET / HTTP/1.1" 200 1 "http://x/\"path\"?a" "ag\\ent"`)
	if err != nil {
		t.Fatal(err)
	}
	if e.Referer != `http://x/"path"?a` {
		t.Errorf("referer = %q", e.Referer)
	}
	if e.Agent != `ag\ent` {
		t.Errorf("agent = %q", e.Agent)
	}
}

func TestMalformed(t *testing.T) {
	bad := []string{
		``,
		`no brackets at all`,
		`203.0.113.9 - - [02/Jan/2006:15:04:05 -0700] "GET / HTTP/1.1" 200 10 extra garbage "more" "and more"`,
		`203.0.113.9 - - [02/Jan/2006:15:04:05 -0700] "GET / HTTP/1.1" 999 10`,
		`203.0.113.9 - - [02/Jan/2006:15:04:05 -0700] "GET / HTTP/1.1" 200 10 "unterminated`,
		`203.0.113.9 - - [02/Jan/2006:15:04:05 -0700] "GET" 200 10`,
		`203.0.113.9 - - [garbage] "GET / HTTP/1.1" 200 10`,
		`a b c d e 203.0.113.9 - - [02/Jan/2006:15:04:05 -0700] "GET / HTTP/1.1" 200 10`,
		`203.0.113.9 - - [02/Jan/2006:15:04:05 -0700] "GET / HTTP/1.1" 200 -1`,
		`203.0.113.9 - - [02/Jan/2006:15:04:05 -0700] "GET / HTTP/1.1" 200`,
		`203.0.113.9 - - [02/Jan/2006:15:04:05 -0700] "GET / HTTP/1.1" 200 99999999999999999999`,
		`203.0.113.9 - - [02/Jan/2006:15:04:05 -0700] "GET / HTTP/1.1" 60 10`,
	}
	for _, l := range bad {
		if _, err := ParseLine(l); err == nil {
			t.Errorf("expected error for %q", l)
		}
	}
}

func TestQueryStripped(t *testing.T) {
	e, err := ParseLine(`10.0.0.1 - - [02/Jan/2006:15:04:05 -0700] "GET /p?sid=abc&k=v HTTP/1.1" 200 1`)
	if err != nil {
		t.Fatal(err)
	}
	if e.Path != "/p" {
		t.Errorf("path = %q", e.Path)
	}
}

var benchLine = combinedLine

func BenchmarkParseLine(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := ParseLine(benchLine); err != nil {
			b.Fatal(err)
		}
	}
}

// Self-check: the parser must be safe on arbitrary input (never panics).
func TestNoPanicArbitrary(t *testing.T) {
	for _, s := range []string{`"`, `"x`, `\`, `[`, `]`, `[x`, `[x] y`, `x [y] "GET / H" 200 1 "a" "b"`, strings.Repeat(`a`, 1<<20)} {
		_, _ = ParseLine(s)
	}
}

func TestCRLFAndTokenBoundaries(t *testing.T) {
	if _, err := ParseLine(combinedLine + "\r"); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		`127.0.0.1 - - [13/Nov/2026:08:00:00 +0000] "GET / HTTP/1.1" 200 42"ref" "agent"`,
		`127.0.0.1 - - [13/Nov/2026:08:00:00 +0000] "GET / HTTP/1.1" 200-`,
	} {
		if _, err := ParseLine(line); err == nil {
			t.Fatalf("accepted missing separator: %s", line)
		}
	}
}

func FuzzParseLine(f *testing.F) {
	f.Add(combinedLine)
	f.Add(strings.Repeat("[", 10000) + "]")
	f.Add(`127.0.0.1 - - [13/Nov/2026:08:00:00 +0000] "-" 400 -`)
	f.Fuzz(func(t *testing.T, line string) { _, _ = ParseLine(line) })
}

func BenchmarkEscapedAgent(b *testing.B) {
	line := `127.0.0.1 - - [13/Nov/2026:08:00:00 +0000] "GET / HTTP/1.1" 200 42 "-" "` + strings.Repeat(`\\`, 10000) + `"`
	b.SetBytes(int64(len(line)))
	for i := 0; i < b.N; i++ {
		if _, err := ParseLine(line); err != nil {
			b.Fatal(err)
		}
	}
}
