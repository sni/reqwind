package reqwind

import (
	"bytes"
	"context"
	"encoding/base64"
	"html"
	"regexp"
	"strings"
	"testing"
	"time"

	"reqwind/assets"
)

func mustRender(t *testing.T, r Report) string {
	t.Helper()
	var buf bytes.Buffer
	if err := RenderReport(&buf, r); err != nil {
		t.Fatalf("RenderReport: %v", err)
	}
	return buf.String()
}

func TestTopPathsTraffic(t *testing.T) {
	r := Report{Days: []Day{
		{Date: "2026-11-01", Vhost: "one", Paths: map[string]int64{"/popular": 20}, PathBytes: map[string]int64{"/large": 2048, "/popular": 20}},
		{Date: "2026-11-01", Vhost: "two", PathBytes: map[string]int64{"/large": 1024}},
		{Date: "2026-11-02", PathBytes: map[string]int64{"/popular": 10, `<img src=x>`: 1}},
	}}
	d := buildData(r)
	if len(d.TopPathsBytes) != 3 || d.TopPathsBytes[0].Name != "/large" || d.TopPathsBytes[0].Count != 3072 || d.TopPathsBytes[1].Count != 30 {
		t.Fatalf("wrong traffic ranking: %+v", d.TopPathsBytes)
	}
	out := mustRender(t, r)
	for _, want := range []string{`id="paths-traffic"`, `Top paths by traffic`, `3.0 KB`, `&lt;img src=x&gt;`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(out, `<img src=x>`) {
		t.Fatal("path traffic label was not escaped")
	}
}

func TestTopPathsTrafficWindow(t *testing.T) {
	days := []Day{
		{Date: "2025-11-30", Bytes: 1000, PathBytes: map[string]int64{"/old": 900, "/shared": 100}},
		{Date: "2025-12-01", Bytes: 20, PathBytes: map[string]int64{"/shared": 20}},
		{Date: "2026-11-15", Bytes: 30, PathBytes: map[string]int64{"/shared": 30}},
	}
	for _, tc := range []struct {
		name   string
		window []Day
		count  int
		first  top
	}{
		{"index", trailingTwelveMonths(days), 1, top{"/shared", 50}},
		{"detail", nil, 2, top{"/old", 900}},
		{"empty window", []Day{}, 0, top{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := buildData(Report{Days: days, ChartDays: tc.window})
			if len(d.TopPathsBytes) != tc.count {
				t.Fatalf("path traffic = %v", d.TopPathsBytes)
			}
			if tc.count > 0 && d.TopPathsBytes[0] != tc.first {
				t.Fatalf("top path traffic = %v, want %v", d.TopPathsBytes[0], tc.first)
			}
			if d.T.Bytes != 1050 {
				t.Fatalf("summary traffic = %d, want full history (1050)", d.T.Bytes)
			}
		})
	}
}

func TestXSSEscaping(t *testing.T) {
	r := Report{
		Generated: time.Date(2026, 11, 12, 8, 30, 0, 0, time.UTC),
		Days: []Day{{
			Date:     "2026-11-12",
			Requests: 3,
			Paths:    map[string]int64{`<script>alert(1)</script>`: 1},
			Referrers: map[string]int64{
				`https://attacker.evil/x?next=<img onerror=alert(2)>`: 1,
			},
			Agents: map[string]int64{`bot/1.0 (https://tracker.evil)` + ` <iframe src=x>`: 1},
			Status: map[string]int64{"200": 3},
		}},
	}
	out := mustRender(t, r)

	for _, want := range []string{
		`&lt;script&gt;alert(1)&lt;/script&gt;`,
		`&lt;img onerror=alert(2)&gt;`,
		`&lt;iframe src=x&gt;`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("escaped output missing %q", want)
		}
	}
	for _, banned := range []string{`<img onerror`, `<iframe src=x>`, `<b>`, `<script>alert(1)</script>`} {
		if strings.Contains(out, banned) {
			t.Errorf("output contains live tag/fragment %q", banned)
		}
	}
	if !strings.Contains(out, `id="palette-select"`) {
		t.Error("palette select missing")
	}
}

func TestNoExternalResources(t *testing.T) {
	r := Report{
		Generated: time.Date(2026, 11, 12, 8, 30, 0, 0, time.UTC),
		Days: []Day{{
			Date:      "2026-11-12",
			Requests:  1,
			Paths:     map[string]int64{"/css/evil.js": 1},
			Referrers: map[string]int64{`https://cdn.evil.example/lib.js`: 1},
			Agents:    map[string]int64{`loader https://fonts.evil.example/x`: 1},
		}},
	}
	out := mustRender(t, r)
	// Assets stay inline; navigation uses local report links and fragments.
	document := regexp.MustCompile(`(?s)(<script\b[^>]*>).*?(</script>)`).ReplaceAllString(out, "$1$2")
	for _, banned := range []string{
		`<iframe`, `<object`, `<embed`,
		`src="http`, `src="//`, `href="http`, `href="//`, `srcset`,
		`<form`, `javascript:alert`, `onerror=`, `onload=`,
	} {
		if strings.Contains(document, banned) {
			t.Errorf("output contains external-resource or clickable-link construct %q", banned)
		}
	}
	// The favicon is an inline data-URI, not a remote file.
	if !strings.Contains(html.UnescapeString(out), `rel="icon" href="data:image/svg+xml;base64,`) {
		t.Error("favicon must be an inline data-URI")
	}
	if !strings.Contains(out, "https://cdn.evil.example/lib.js") {
		t.Error("referrer URL should still be visible as plain text")
	}
}

func TestEmptyReport(t *testing.T) {
	out := mustRender(t, Report{})
	for _, banned := range []string{"<nil>", "invalid character"} {
		if strings.Contains(out, banned) {
			t.Errorf("empty report output contains %q", banned)
		}
	}
	if !strings.Contains(out, "No data.") {
		t.Error("empty report should render placeholder text")
	}
	if !strings.Contains(out, `<span class="num" title="0">0</span>`) {
		t.Error("empty report should show zero totals")
	}
	// The OS and status pie anchors sit beside their tables even when empty.
	if !strings.Contains(out, `id="rw-chart-os"`) || !strings.Contains(out, `id="rw-chart-status"`) {
		t.Error("OS/status pie anchors missing from the report grid")
	}
}

func TestLiveRefresh(t *testing.T) {
	off := mustRender(t, Report{})
	if strings.Contains(off, `http-equiv="refresh"`) {
		t.Error("non-live report must not contain meta refresh")
	}
	on := mustRender(t, Report{Live: true})
	if !strings.Contains(on, `<meta http-equiv="refresh" content="5">`) {
		t.Error("live report must contain meta refresh tag")
	}
}

func TestTotalsAndVisitorUnion(t *testing.T) {
	r := Report{Days: []Day{
		{Date: "2026-11-11", Requests: 5, Bytes: 2048, Visitors: map[string]bool{"a": true, "b": true}},
		{Date: "2026-11-12", Requests: 7, Bytes: 4096, Visitors: map[string]bool{"b": true}},
	}}
	out := mustRender(t, r)
	if !strings.Contains(out, `<span class="num" title="12">12</span>`) {
		t.Error("total requests should be the sum of days (12)")
	}
	if !strings.Contains(out, `<span class="num" title="6144">6.0 KB</span>`) {
		t.Error("total bytes should be the sum of days (2048+4096 = 6.0 KB)")
	}
	if !strings.Contains(out, `<span class="num" title="2">2</span>`) {
		t.Error("unique visitors should be the union across days (a,b = 2)")
	}
}
func TestLargeByteUnits(t *testing.T) {
	if got := humanBytes(1 << 50); got != "1.0 PB" {
		t.Fatalf("got %s", got)
	}
	if got := humanBytes(1 << 60); got != "1.0 EB" {
		t.Fatalf("got %s", got)
	}
}

func TestDetailedMetricTotals(t *testing.T) {
	r := Report{Days: []Day{
		{Date: "2026-11-10", Requests: 4, Bytes: 100, Files: 2, Pages: 1, Visits: 1},
		{Date: "2026-11-11", Requests: 6, Bytes: 200, Files: 3, Pages: 2, Visits: 1},
	}}
	d := buildData(r)
	if d.T.Files != 5 || d.T.Pages != 3 || d.T.Visits != 2 {
		t.Fatalf("wrong detailed totals: %+v", d.T)
	}
	out := mustRender(t, r)
	for _, want := range []string{`<span class="num" title="5">5</span>`, `<span class="num" title="3">3</span>`, `<span class="num" title="2">2</span>`} {
		if !strings.Contains(out, want) {
			t.Errorf("detailed total missing %s", want)
		}
	}
}

func TestDailyAveragesIncludeGaps(t *testing.T) {
	// Only two data days, three calendar days apart → denominator 3.
	r := Report{Days: []Day{
		{Date: "2026-11-01", Requests: 9, Bytes: 3072, Files: 5, Pages: 3, Visits: 2, Visitors: map[string]bool{"same": true}},
		{Date: "2026-11-03", Requests: 0, Bytes: 0},
		{Date: "2026-11-04", Requests: 6, Bytes: 2048, Files: 2, Pages: 2, Visits: 1, Visitors: map[string]bool{"same": true}},
	}}
	d := buildData(r)
	if d.AvgDenominator != 4 {
		t.Fatalf("denominator should span 01..04 calendar days, got %d", d.AvgDenominator)
	}
	if d.AvgRequests != 3.75 || d.AvgBytes != 1280 || d.AvgFiles != 1.75 || d.AvgPages != 1.25 || d.AvgVisits != 0.75 || d.AvgVisitors != 0.5 {
		t.Fatalf("wrong averages: %+v", d)
	}
	out := mustRender(t, r)
	if !strings.Contains(out, "Across 4 calendar days") || !strings.Contains(out, "3.75") || !strings.Contains(out, "1.2 KB") || !strings.Contains(out, "0.000043") || !strings.Contains(out, "0.015 B/s") {
		t.Fatal("average cards missing calendar-day denominator label")
	}
	// The rate column is named "rate"; only hits and traffic carry "/s".
	if !strings.Contains(out, `class="num">Rate</th>`) {
		t.Fatal("daily averages table must use the Rate header")
	}
	if got := strings.Count(out, "/s</td>"); got != 2 {
		t.Fatalf("only hits and traffic may carry /s, got %d cells", got)
	}
	if !strings.Contains(out, `>0.015 B/s</td>`) {
		t.Fatal("traffic rate cell must carry the /s unit")
	}
	// Gap days are not dropped from the daily table.
	if !strings.Contains(out, `>2026-11-03<`) {
		t.Fatal("zero-traffic gap day must remain in the daily table")
	}
}

func TestPeriodLine(t *testing.T) {
	// The header shows the data span under the title; no line when empty.
	d := buildData(Report{Days: []Day{
		{Date: "2026-01-01", Requests: 1}, {Date: "2026-02-01", Requests: 2},
	}})
	if d.Period != "2026-01-01 – 2026-02-01" {
		t.Fatalf("period = %q", d.Period)
	}
	if !strings.Contains(mustRender(t, Report{Days: d.Days}), `>2026-01-01 – 2026-02-01</p>`) {
		t.Fatal("period line missing under the title")
	}
	if strings.Contains(mustRender(t, Report{}), `class="period"`) {
		t.Fatal("empty report must not render a period line")
	}
}

func TestStatusNames(t *testing.T) {
	d := buildData(Report{Days: []Day{{
		Date:   "2026-11-01",
		Status: map[string]int64{"200": 5, "499": 2, "999": 1},
	}}})
	want := map[string]int64{"200 OK": 5, "499 Unknown Status": 2, "999 Unknown Status": 1}
	for _, e := range d.TopStatus {
		if want[e.Name] != e.Count {
			t.Fatalf("status %q: got count %d want %d", e.Name, e.Count, want[e.Name])
		}
		delete(want, e.Name)
	}
	if len(want) != 0 {
		t.Fatalf("missing status entries: %v", want)
	}
}

func TestTopClientsBothRankings(t *testing.T) {
	// a wins on requests, b wins on bytes; c/d tie on requests.
	d := buildData(Report{Days: []Day{
		{Date: "2026-11-01", Requests: 4, Clients: map[string]ClientStats{"a": {Requests: 1, Bytes: 10}, "b": {Requests: 1, Bytes: 100}}},
		{Date: "2026-11-02", Requests: 9, Vhost: "other.example", Clients: map[string]ClientStats{"a": {Requests: 9, Bytes: 90}, "b": {Requests: 4, Bytes: 4900}, "c": {Requests: 7, Bytes: 700}, "d": {Requests: 7, Bytes: 600}}},
	}})
	if len(d.TopClientsRequests) != 4 || len(d.TopClientsBytes) != 4 {
		t.Fatalf("client lists: req=%d bytes=%d", len(d.TopClientsRequests), len(d.TopClientsBytes))
	}
	if d.TopClientsRequests[0].Name != "a" || d.TopClientsRequests[0].Requests != 10 {
		t.Fatalf("requests ranking wrong: %+v", d.TopClientsRequests)
	}
	if d.TopClientsBytes[0].Name != "b" || d.TopClientsBytes[0].Bytes != 5000 {
		t.Fatalf("bytes ranking wrong: %+v", d.TopClientsBytes)
	}
	// Deterministic tie: c (700 bytes) sorts before d (600 bytes) on bytes,
	// and by name on the requests ranking.
	if d.TopClientsRequests[1].Name != "c" || d.TopClientsRequests[2].Name != "d" {
		t.Fatalf("requests tie not deterministic: %+v", d.TopClientsRequests[1:3])
	}
	if d.TopClientsBytes[1].Name != "c" || d.TopClientsBytes[2].Name != "d" {
		t.Fatalf("bytes tie not deterministic: %+v", d.TopClientsBytes[1:3])
	}
	// Client IDs are hashed identifiers; the full hash is shown, not a raw IP.
	out := mustRender(t, Report{Days: []Day{{Date: "2026-11-01", Requests: 1, Clients: map[string]ClientStats{"abc123def456": {Requests: 1, Bytes: 1}}}}})
	if !strings.Contains(out, "abc123def456") {
		t.Fatal("hashed client identifier must be shown in full")
	}
}

func TestCountriesTable(t *testing.T) {
	days := []Day{{Date: "2026-11-01", Requests: 3, Countries: map[string]int64{"DE": 2, "FR": 1}}}
	on := mustRender(t, Report{Days: days, GeoEnabled: true})
	if !strings.Contains(on, ">DE<") || !strings.Contains(on, ">FR<") {
		t.Fatal("country table must list countries with data")
	}
	off := mustRender(t, Report{Days: days, GeoEnabled: false})
	if !strings.Contains(off, "Geolocation disabled for new records") || !strings.Contains(off, ">DE<") {
		t.Fatal("existing country counts must survive disabled lookups")
	}
	if !strings.Contains(mustRender(t, Report{}), "Local GeoIP database not configured") {
		t.Fatal("missing no-database explanation")
	}
}

func TestPaletteInjectionAndFailsafe(t *testing.T) {
	for _, p := range []string{"ocean", "forest", "ember"} {
		out := mustRender(t, Report{Palette: p})
		if !strings.Contains(out, `data-palette="`+p+`"`) {
			t.Fatalf("palette %q not in data-palette attribute", p)
		}
		if !strings.Contains(out, `value="`+p+`" selected`) || !strings.Contains(out, "prefers-color-scheme:dark") {
			t.Fatal("missing no-JS default or automatic dark mode")
		}
	}
	// Unknown or empty values fall back to ocean; nothing untrusted reaches the DOM.
	for _, p := range []string{"", "ocean;}", "ember onmouseover=alert(1)", "not-a-palette"} {
		d := buildData(Report{Palette: p})
		if d.Palette != "ocean" {
			t.Fatalf("palette %q must fail safe to ocean, got %q", p, d.Palette)
		}
	}
	out := mustRender(t, Report{})
	if !strings.Contains(out, `<html lang="en" data-palette="ocean">`) {
		t.Fatal("default palette must be ocean")
	}
	// The select is present and labelled for accessibility.
	if !strings.Contains(out, `aria-label="Colour scheme"`) {
		t.Fatal("palette select needs an accessible label")
	}
	// The theme toggle and the dark/light overrides ship with the page.
	if !strings.Contains(out, `id="theme-toggle"`) || !strings.Contains(out, `:root[data-theme=dark]`) {
		t.Fatal("theme toggle or manual theme CSS missing")
	}
	// The site select renders only with at least two vhosts and marks the
	// current page as selected.
	if strings.Contains(out, `id="site-select"`) {
		t.Fatal("site select must stay hidden without vhosts")
	}
	multi := mustRender(t, Report{Title: "a.example", Vhosts: []Summary{
		{Name: "a.example", Href: "a.html"}, {Name: "b.example", Href: "b.html"},
	}})
	if !strings.Contains(multi, `id="site-select"`) || !strings.Contains(multi, `<option value="a.html" selected>a.example</option>`) {
		t.Fatal("site select must list vhosts and preselect the current one")
	}
}

func TestHeaderNavigationAndThemeRow(t *testing.T) {
	// A page with an index link makes the brand (logo + title) a link to the
	// all-sites index; the index itself has no brand link.
	sub := mustRender(t, Report{Title: "2026", IndexHref: "index.html"})
	if !strings.Contains(sub, `<a href="index.html" aria-label="All sites">`) {
		t.Fatal("sub-page brand must link to the all-sites index")
	}
	idx := mustRender(t, Report{Title: "All sites"})
	if strings.Contains(idx, `aria-label="All sites"><img`) {
		t.Fatal("index brand must not be a link to itself")
	}
	// The theme toggle now lives inside the settings panel with a visible label.
	if !strings.Contains(sub, `class="theme-row"`) || !strings.Contains(sub, `id="theme-name"`) {
		t.Fatal("theme toggle must sit in the settings panel with a label")
	}
	if !strings.Contains(sub, `id="theme-toggle"`) {
		t.Fatal("theme toggle button missing")
	}
}

func TestThousandsGrouping(t *testing.T) {
	for in, want := range map[int64]string{
		0: "0", 5: "5", 999: "999", 1000: "1,000", 1234: "1,234",
		12345: "12,345", 123456: "123,456", 1234567: "1,234,567",
	} {
		if got := group(in); got != want {
			t.Errorf("group(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestRawClientIDs(t *testing.T) {
	// With a raw name, the report shows the IP instead of the hash.
	r := Report{Days: []Day{{Date: "2026-11-01", Requests: 1,
		Clients: map[string]ClientStats{"abc123def456": {Requests: 1, Bytes: 1, Raw: "10.0.0.5"}}}}}
	out := mustRender(t, r)
	if !strings.Contains(out, "10.0.0.5") || strings.Contains(out, "abc123def456") {
		t.Fatal("raw IP must replace the hash in the client table")
	}
	if !strings.Contains(out, "Raw client IPs") {
		t.Fatal("raw-IP warning missing")
	}
	// Without raw names, the hash and the privacy note remain.
	plain := mustRender(t, Report{Days: []Day{{Date: "2026-11-01", Requests: 1,
		Clients: map[string]ClientStats{"abc123def456": {Requests: 1, Bytes: 1}}}}})
	if !strings.Contains(plain, "abc123def456") || !strings.Contains(plain, "Hashed identifiers") {
		t.Fatal("hashed client table must keep its privacy note")
	}
}

func TestVersionBrandingAndNavigation(t *testing.T) {
	var version bytes.Buffer
	if err := Run(context.Background(), []string{"-version"}, &version); err != nil || version.String() != "reqwind 0.0.1\n" {
		t.Fatalf("version: %q, %v", version.String(), err)
	}
	generated := time.Date(2026, 11, 13, 10, 20, 30, 0, time.UTC)
	// ShowDrilldown is on here so the navigation anchor is present; the
	// all-sites index turns it off and the section is skipped.
	out := html.UnescapeString(mustRender(t, Report{Generated: generated, ShowDrilldown: true}))
	for _, want := range []string{
		"generated by reqwind 0.0.1 · " + generated.Local().Format("2006-01-02 15:04:05 MST"),
		base64.StdEncoding.EncodeToString(assets.Logo), base64.StdEncoding.EncodeToString(assets.Icon),
		`aria-label="Report sections"`, `href="#path-drilldown"`, `id="path-drilldown"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	// Third-party licenses live in the README and assets/, not in every page.
	if strings.Contains(out, `id="third-party-licenses"`) || strings.Contains(out, "Apache ECharts Subcomponents") {
		t.Error("full license text must not be embedded in pages")
	}
	ids := map[string]bool{}
	for _, match := range regexp.MustCompile(`id="([^"]+)"`).FindAllStringSubmatch(out, -1) {
		ids[match[1]] = true
	}
	for _, match := range regexp.MustCompile(`href="#([^"]+)"`).FindAllStringSubmatch(out, -1) {
		if !ids[match[1]] {
			t.Errorf("navigation target missing: %s", match[1])
		}
	}
}

func TestHumanAverageBytes(t *testing.T) {
	for value, want := range map[float64]string{0: "0.0 B", 0.5: "0.5 B", 1280: "1.2 KB", 1048576: "1.0 MB", float64(1<<63 - 1): "8.0 EB"} {
		if got := humanAverageBytes(value); got != want {
			t.Errorf("%v: got %q, want %q", value, got, want)
		}
	}
}

func TestHumanByteRate(t *testing.T) {
	for value, want := range map[float64]string{0: "0.000 B/s", 0.015: "0.015 B/s", 1280: "1.2 KB/s"} {
		if got := humanByteRate(value); got != want {
			t.Errorf("%v: got %q, want %q", value, got, want)
		}
	}
}

func TestReportOnlyShowsExternalReferrers(t *testing.T) {
	d := buildData(Report{Days: []Day{
		{Date: "2026-11-12", Vhost: "example.org:443", Referrers: map[string]int64{"https://example.org/self": 4, "https://sub.example.org/inside": 2, "https://outside.org/link": 3}},
		{Date: "2026-11-12", Vhost: "other.org", Referrers: map[string]int64{"https://example.org/from-other": 5}},
		{Date: "2026-11-12", Vhost: "log:access.log [01234567]", Referrers: map[string]int64{"https://shop.example/self": 9, "https://news.example/story": 1}},
	}})
	got := map[string]int64{}
	for _, ref := range d.TopRefs {
		got[ref.Name] = ref.Count
	}
	if len(got) != 3 || got["https://outside.org/link"] != 3 || got["https://example.org/from-other"] != 5 || got["https://news.example/story"] != 1 {
		t.Fatalf("wrong external referrer totals: %v", got)
	}
}

func TestReportTimestampUsesLocalTimezone(t *testing.T) {
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	previous := time.Local
	time.Local = zone
	t.Cleanup(func() { time.Local = previous })
	out := mustRender(t, Report{Generated: time.Date(2026, 11, 13, 10, 20, 30, 0, time.UTC), Days: []Day{{Date: "2026-11-13", Requests: 1, Hours: [24]int64{1}, Clients: map[string]ClientStats{"id": {Requests: 1, Raw: "192.0.2.1"}}}}})
	if !strings.Contains(out, "2026-11-13 05:20:30 EST") {
		t.Error("report timestamp must use the local timezone")
	}
}
