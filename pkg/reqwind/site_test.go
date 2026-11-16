package reqwind

import (
	"encoding/json"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestInferSiteName(t *testing.T) {
	fallback := "log:access.log [01234567]"
	// Explicit -vhost and parsed vhost prefix are plain hostnames: never
	// rewritten, regardless of referrers.
	if got := inferSiteName("example.org", nil); got != "example.org" {
		t.Fatalf("explicit vhost rewritten: %q", got)
	}
	// Strict majority of valid http(s) referrers names the group.
	days := []Day{{Referrers: map[string]int64{
		"https://shop.example/a": 4,
		"https://other.org/b":    1,
	}}}
	if got := inferSiteName("log:443", days); got != "log:443" {
		t.Fatalf("explicit host:port rewritten: %q", got)
	}
	if got := inferSiteName(fallback, days); got != "shop.example" {
		t.Fatalf("majority host = %q", got)
	}
	// A tie yields no inference (falls back to the machine hostname).
	tie := []Day{{Referrers: map[string]int64{
		"https://a.example/x": 2,
		"https://b.example/y": 2,
	}}}
	if got := inferSiteName(fallback, tie); got != "" {
		t.Fatalf("tie must not infer a site: %q", got)
	}
	// Malformed, non-http, and search-engine referrers are excluded; an empty
	// or search-only pool yields no inference.
	malformed := []Day{{Referrers: map[string]int64{
		"not a url":                            5,
		"ftp://files.example/":                 5,
		"https://www.google.com/search?q=shop": 5,
	}}}
	if got := inferSiteName(fallback, malformed); got != "" {
		t.Fatalf("invalid refs must not infer a site: %q", got)
	}
	// A single valid external referrer over an excluded one is a strict majority.
	one := []Day{{Referrers: map[string]int64{
		"https://www.google.com/search?q=shop": 9,
		"https://shop.example/p":               1,
	}}}
	if got := inferSiteName(fallback, one); got != "shop.example" {
		t.Fatalf("search referrers must not outvote the site: %q", got)
	}
}

func TestInferSiteNameBuildPagesDisplayOnly(t *testing.T) {
	// A fallback group (log: prefix) gets the inferred display name, while the
	// explicit vhost keeps its own. Grouping and the stable href hash are
	// unchanged.
	r := Report{Days: []Day{
		{Date: "2026-01-01", Vhost: "log:access.log [deadbeef]", Requests: 3,
			Referrers: map[string]int64{"https://shop.example/a": 3}},
		{Date: "2026-01-01", Vhost: "example.org", Requests: 2,
			Referrers: map[string]int64{"https://shop.example/a": 9}},
	}}
	pages := BuildPages("/reports/index.html", r)
	// The two groups must be named "shop.example" (inferred fallback) and
	// "example.org" (explicit, never rewritten), each with a distinct href.
	names := map[string]bool{}
	hrefs := map[string]bool{}
	for _, v := range pages[0].Report.Vhosts {
		names[v.Name] = true
		hrefs[v.Href] = true
	}
	if !names["shop.example"] || !names["example.org"] {
		t.Fatalf("vhost names = %v, want shop.example + example.org", names)
	}
	if len(hrefs) != 2 {
		t.Fatalf("want 2 distinct vhost hrefs, got %v", hrefs)
	}
}

func archiveFixture() Report {
	return Report{Days: []Day{
		{Date: "2025-12-31", Vhost: "one.example", Requests: 2, Bytes: 1024, Visitors: map[string]bool{"a": true}},
		{Date: "2026-01-01", Vhost: "one.example", Requests: 3, Bytes: 2048, Visitors: map[string]bool{"a": true, "b": true}, Paths: map[string]int64{"/": 3}, Terms: map[string]int64{"go logs": 2}},
		{Date: "2026-01-01", Vhost: "two.example", Requests: 4, Bytes: 4096, Visitors: map[string]bool{"a": true, "c": true}, Paths: map[string]int64{"/": 4}},
		{Date: "2026-02-01", Vhost: "two.example", Requests: 5, Bytes: 8192, Visitors: map[string]bool{"a": true, "b": true}},
	}}
}

func TestArchivePagesAndUnions(t *testing.T) {
	r := archiveFixture()
	before, _ := json.Marshal(r)
	pages := BuildPages("/reports/index.html", r)
	// index + 2 all-sites years + 3 all-sites months + 2 vhost pages +
	// per-vhost sub-pages: one.example(2025,2026:1y; 2025-12,2026-01:2m) and
	// two.example(2026:1y; 2026-01,2026-02:2m) = 3+3 = 6 sub-pages.
	if len(pages) != 15 {
		t.Fatalf("got %d pages want 15 (index + all-sites + vhost + sub-pages)", len(pages))
	}
	index := pages[0].Report
	if index.MonthlyCharts {
		t.Fatal("index must not render daily charts")
	}
	if s := index.Years[0]; s.Name != "2026" || s.Requests != 12 || s.Bytes != 14336 || s.Visitors != 3 {
		t.Fatalf("wrong year summary: %+v", s)
	}
	if s := index.Months[1]; s.Name != "2026-01" || s.Requests != 7 || s.Visitors != 3 {
		t.Fatalf("wrong month summary: %+v", s)
	}
	if s := index.Vhosts[0]; s.Requests != 5 || s.Visitors != 2 {
		t.Fatalf("wrong host summary: %+v", s)
	}
	data := buildData(index)
	if data.T.Requests != 14 || data.T.Visitors != 3 || len(data.Days) != 3 {
		t.Fatalf("wrong daily consolidation: %+v", data.T)
	}
	if d := data.Days[1]; d.Requests != 7 || len(d.Visitors) != 3 || d.Paths["/"] != 7 {
		t.Fatalf("wrong merged day: %+v", d)
	}
	// The charts render as one combined interactive block (legacy SVG data
	// charts are gone); the index with data must produce non-empty chart HTML.
	if len(data.InteractiveCharts) == 0 {
		t.Fatal("index report with days must render the combined chart payload")
	}
	if chart := extractChartData(t, data.InteractiveCharts); chart.Unit != "day" || len(chart.Labels) != 3 {
		t.Fatalf("index must show daily values: %+v", chart)
	}
	exists := map[string]bool{}
	for _, p := range pages {
		exists[filepath.Base(p.Path)] = true
	}
	links := regexp.MustCompile(`href="([^"]+)"`)
	for _, p := range pages {
		// Only the base vhost page (not its year/month sub-pages) is a hub
		// summarizing its own single host.
		fn := filepath.Base(p.Path)
		isBaseVhost := strings.Contains(fn, "-vhost-") && !strings.Contains(fn, "-year-") && !strings.Contains(fn, "-month-")
		if isBaseVhost {
			summaries := buildData(p.Report).VhostSummary
			if len(summaries) != 1 || summaries[0].Name != p.Report.Title || len(p.Report.Vhosts) != 2 {
				t.Fatalf("vhost page must summarize its own host but keep all navigation tabs: %+v", p.Report)
			}
			// The vhost hub's year/month tables link to its own sub-pages.
			for _, s := range p.Report.Years {
				if !strings.Contains(s.Href, "-vhost-") || !strings.Contains(s.Href, "-year-") {
					t.Fatalf("vhost page year link must target its sub-page: %q", s.Href)
				}
			}
			for _, s := range p.Report.Months {
				if !strings.Contains(s.Href, "-vhost-") || !strings.Contains(s.Href, "-month-") {
					t.Fatalf("vhost page month link must target its sub-page: %q", s.Href)
				}
			}
		}
		html := mustRender(t, p.Report)
		for _, link := range links.FindAllStringSubmatch(html, -1) {
			switch {
			case strings.HasPrefix(link[1], "data:"):
				// inline favicon / logo
			case strings.HasPrefix(link[1], "#"):
				// sidebar anchor fragment
			default:
				if !exists[link[1]] {
					t.Fatalf("broken link in %s: %s", p.Path, link[1])
				}
			}
		}
		// The only <link> allowed is the inline data-URI favicon.
		if got := strings.Count(html, `<link`); got > 1 {
			t.Fatalf("multiple <link> tags in %s", p.Path)
		}
		// The favicon is an inline data-URI. The exact MIME string is HTML
		// attribute-escaped (+ becomes &#43;), so match the stable prefix only.
		if !strings.Contains(html, `rel="icon" href="data:image`) {
			t.Fatalf("missing inline favicon in %s", p.Path)
		}
		for _, bad := range []string{"#ZgotmplZ", "src=\"http"} {
			if strings.Contains(html, bad) {
				t.Fatalf("unsafe report: %s", bad)
			}
		}
		if !strings.Contains(html, `id="palette-select"`) {
			t.Fatal("palette select missing")
		}
		for _, want := range []string{`id="site-select"`, `id="theme-toggle"`} {
			if !strings.Contains(html, want) {
				t.Fatalf("missing %s", want)
			}
		}
		// The combined charts and the section anchors replace the per-metric
		// SVG chart titles; at least one chart anchor and the search-terms
		// section must render.
		for _, want := range []string{`id="activity-chart"`, `href="#activity-chart"`, "Search engine terms"} {
			if !strings.Contains(html, want) {
				t.Fatalf("missing %s", want)
			}
		}
		if p.Path != pages[0].Path && (p.Report.MonthlyCharts || !strings.Contains(html, "All sites · index")) {
			t.Fatal("detail page lacks daily charts/index link")
		}
		// The all-sites index hides the oversized prefix drilldown; every
		// detail page keeps it.
		wantDrilldown := p.Path != pages[0].Path
		if has := strings.Contains(html, `id="path-drilldown"`); has != wantDrilldown {
			t.Fatalf("drilldown visibility wrong for %s", p.Path)
		}
	}
	after, _ := json.Marshal(r)
	if string(before) != string(after) {
		t.Fatal("report generation mutated persistent input")
	}
}

func TestPeriodPagesUsePerPeriodVhostTotals(t *testing.T) {
	// Year and month pages must carry their own per-host rows, not the
	// all-time totals of the vhost page list.
	r := archiveFixture()
	pages := BuildPages("/reports/index.html", r)
	byPage := map[string]Report{}
	for _, p := range pages[1:] {
		byPage[filepath.Base(p.Path)] = p.Report
	}
	// check asserts a page's vhost table carries its own per-host totals and
	// links each row to the matching per-vhost sub-page (period suffix).
	check := func(name, page, suffix string, want map[string]Summary) {
		t.Helper()
		rep, ok := byPage[page]
		if !ok {
			t.Fatalf("%s page missing", name)
		}
		got := map[string]Summary{}
		for _, s := range rep.VhostSummary {
			got[s.Name] = s
		}
		for host, s := range want {
			if g := got[host]; g.Requests != s.Requests || g.Bytes != s.Bytes || g.Visitors != s.Visitors {
				t.Fatalf("%s: host %s got %+v want %+v", name, host, g, s)
			}
			if !strings.HasSuffix(got[host].Href, suffix) {
				t.Fatalf("%s: host %s href %q must point to the per-vhost %s sub-page", name, host, got[host].Href, suffix)
			}
		}
		if len(got) != len(want) {
			t.Fatalf("%s: %d rows want %d: %v", name, len(got), len(want), got)
		}
	}
	year2026 := "index-year-2026.html"
	february := "index-month-2026-02.html"
	check("year", year2026, "-year-2026.html", map[string]Summary{
		// The 2025-12-31 day belongs to the previous year page.
		"one.example": {Name: "one.example", Requests: 3, Bytes: 2048, Visitors: 2},
		"two.example": {Name: "two.example", Requests: 9, Bytes: 12288, Visitors: 3},
	})
	check("month", february, "-month-2026-02.html", map[string]Summary{
		"two.example": {Name: "two.example", Requests: 5, Bytes: 8192, Visitors: 2},
	})
	// A base vhost page keeps its single host row and no vhost chart.
	for _, p := range pages[1:] {
		fn := filepath.Base(p.Path)
		if !strings.Contains(fn, "-vhost-") || strings.Contains(fn, "-year-") || strings.Contains(fn, "-month-") {
			continue
		}
		if s := p.Report.VhostSummary; len(s) != 1 || s[0].Name != p.Report.Title {
			t.Fatalf("vhost page summary wrong: %+v", s)
		}
	}
}

func TestSummaryHeadersMatchContext(t *testing.T) {
	for _, p := range BuildPages("/reports/index.html", archiveFixture()) {
		t.Run(filepath.Base(p.Path), func(t *testing.T) {
			out := mustRender(t, p.Report)
			for _, tc := range []struct {
				title, header string
				rows          []Summary
			}{
				{"Virtual hosts and log sources", "Virtual host", p.Report.VhostSummary},
				{"Yearly summary", "Period", p.Report.Years},
				{"Monthly summary", "Period", p.Report.Months},
			} {
				if len(tc.rows) == 0 {
					continue
				}
				want := `<h2>` + tc.title + `</h2><table class="report-summary" aria-label="Report summary"><thead><tr><th scope="col">` + tc.header + `</th>`
				if !strings.Contains(out, want) {
					t.Errorf("%s must use the %q header", tc.title, tc.header)
				}
			}
			if strings.Contains(out, "Period / host") {
				t.Error("ambiguous summary header remains")
			}
		})
	}
}

func TestVhostSummaryRowsUsePeriodOnly(t *testing.T) {
	// On a vhost page the yearly/monthly rows name only the period; the
	// page title and the "All sites" row already carry the host name.
	r := archiveFixture()
	pages := BuildPages("/reports/index.html", r)
	for _, p := range pages[1:] {
		fn := filepath.Base(p.Path)
		if !strings.Contains(fn, "-vhost-") || strings.Contains(fn, "-year-") || strings.Contains(fn, "-month-") {
			continue
		}
		for _, s := range p.Report.Years {
			if strings.Contains(s.Name, "one.example") || strings.Contains(s.Name, "two.example") {
				t.Fatalf("vhost year row repeats the host name: %q", s.Name)
			}
		}
		for _, s := range p.Report.Months {
			if strings.Contains(s.Name, "one.example") || strings.Contains(s.Name, "two.example") {
				t.Fatalf("vhost month row repeats the host name: %q", s.Name)
			}
		}
	}
}

func TestIndexWindowedToTwelveMonths(t *testing.T) {
	r := Report{Days: []Day{
		{Date: "2024-12-01", Vhost: "one.example", Requests: 10, Bytes: 1000, Visitors: map[string]bool{"old": true},
			Paths: map[string]int64{"/old": 10}, PathBytes: map[string]int64{"/old": 1000},
			ErrorPaths: map[string]int64{"/old": 10}, Referrers: map[string]int64{"https://old.example/": 10},
			Agents: map[string]int64{"old": 10}, Status: map[string]int64{"500": 10},
			Clients: map[string]ClientStats{"old": {Requests: 10, Bytes: 1000}},
			Terms:   map[string]int64{"old": 10}, Countries: map[string]int64{"DE": 10}},
		{Date: "2026-01-01", Vhost: "one.example", Requests: 5, Bytes: 50, Visitors: map[string]bool{"new": true},
			Paths: map[string]int64{"/new": 5}, PathBytes: map[string]int64{"/new": 50},
			ErrorPaths: map[string]int64{"/new": 5}, Referrers: map[string]int64{"https://new.example/": 5},
			Agents: map[string]int64{"new": 5}, Status: map[string]int64{"404": 5},
			Clients: map[string]ClientStats{"new": {Requests: 5, Bytes: 50}},
			Terms:   map[string]int64{"new": 5}, Countries: map[string]int64{"US": 5}},
	}}
	pages := BuildPages("/reports/index.html", r)
	index := pages[0].Report
	if got := len(index.ChartDays); got != 1 {
		t.Fatalf("index chart window has %d days want 1: %v", got, index.ChartDays)
	}
	if index.ChartDays[0].Date != "2026-01-01" {
		t.Fatalf("index chart window kept wrong day: %v", index.ChartDays[0].Date)
	}
	if got := index.Vhosts[0].Requests; got != 5 {
		t.Fatalf("index host summary must be windowed: %d", got)
	}
	if len(index.Months) != 1 || index.Months[0].Name != "2026-01" {
		t.Fatalf("index monthly summary = %+v", index.Months)
	}
	if len(index.Years) != 2 || index.Years[1].Requests != 10 {
		t.Fatalf("index yearly summary must keep full history: %+v", index.Years)
	}
	data := buildData(index)
	if data.T.Requests != 5 || data.T.Bytes != 50 || data.T.Visitors != 1 ||
		data.AvgDenominator != 1 || data.AvgRequests != 5 || data.AvgBytes != 50 ||
		data.Period != "2026-01-01 – 2026-01-01" {
		t.Fatalf("index totals or date range include old data: totals=%+v, period=%s", data.T, data.Period)
	}
	if len(data.VhostSummary) != 1 || data.VhostSummary[0].Requests != 5 {
		t.Fatalf("index host table = %+v", data.VhostSummary)
	}
	for name, ranking := range map[string][]top{
		"paths": data.TopPaths, "traffic": data.TopPathsBytes, "errors": data.TopErrorPaths,
		"referrers": data.TopRefs, "agents": data.TopAgents, "status": data.TopStatus,
		"terms": data.TopTerms, "countries": data.TopCountries,
	} {
		if len(ranking) != 1 || (ranking[0].Count != 5 && name != "traffic") ||
			(name == "traffic" && ranking[0].Count != 50) {
			t.Errorf("index %s includes old data: %+v", name, ranking)
		}
	}
	for _, ranking := range [][]clientEntry{data.TopClientsRequests, data.TopClientsBytes} {
		if len(ranking) != 1 || ranking[0].Name != "new" || ranking[0].Requests != 5 || ranking[0].Bytes != 50 {
			t.Errorf("index clients include old data: %+v", ranking)
		}
	}
	// The index chart payload labels only the windowed month.
	d := extractChartData(t, chartHTML(index.ChartDays, false))
	if len(d.Labels) != 1 || d.Labels[0] != "2026-01-01" {
		t.Fatalf("index chart labels = %v", d.Labels)
	}
	// A detail page (vhost) is not windowed: it charts the full history.
	var vhost Report
	for _, p := range pages[1:] {
		if strings.Contains(filepath.Base(p.Path), "-vhost-") && !strings.Contains(filepath.Base(p.Path), "-year-") && !strings.Contains(filepath.Base(p.Path), "-month-") {
			vhost = p.Report
		}
	}
	if vhost.ChartDays != nil {
		t.Fatalf("detail page must not be windowed: %v", vhost.ChartDays)
	}
	if data := buildData(vhost); data.T.Requests != 15 || len(data.TopPaths) != 2 {
		t.Fatalf("detail tables must keep full history: %+v", data.T)
	}
	if d := extractChartData(t, chartHTML(vhost.Days, false)); len(d.Labels) != 2 {
		t.Fatalf("detail chart must span full history: %v", d.Labels)
	}
}

func TestHostAndTermEscaping(t *testing.T) {
	host := `../../<img src=x onerror=alert(1)>`
	r := Report{Days: []Day{{Date: "2026-01-01", Vhost: host, Requests: 1, Terms: map[string]int64{`<script>alert(1)</script>`: 1}}}}
	for _, p := range BuildPages("/reports/index.html", r) {
		if filepath.Dir(p.Path) != "/reports" || strings.Contains(filepath.Base(p.Path), "<") {
			t.Fatalf("unsafe page path: %s", p.Path)
		}
		html := mustRender(t, p.Report)
		// The brand logo is a legitimate <img>; the injected host must be the
		// escaped form, never a live tag.
		if strings.Contains(html, "<img src=x onerror") {
			t.Fatal("unescaped host/search term")
		}
		if !strings.Contains(html, "&lt;img src=x onerror=alert(1)&gt;") {
			t.Fatal("host not displayed escaped")
		}
		if !strings.Contains(html, "&lt;script&gt;") {
			t.Fatal("term not displayed")
		}
	}
}

func TestReportLinksEscapeOutputBasename(t *testing.T) {
	pages := BuildPages("/reports/traffic #1?.html", archiveFixture())
	exists := map[string]bool{}
	for _, page := range pages {
		exists[filepath.Base(page.Path)] = true
	}
	check := func(href string) {
		t.Helper()
		u, err := url.Parse(href)
		if err != nil || u.Fragment != "" || u.RawQuery != "" || !exists[u.Path] {
			t.Fatalf("broken escaped link: %q %+v %v", href, u, err)
		}
	}
	for _, page := range pages {
		if page.Report.IndexHref != "" {
			check(page.Report.IndexHref)
		}
		for _, summaries := range [][]Summary{page.Report.Years, page.Report.Months, page.Report.Vhosts, page.Report.VhostSummary} {
			for _, s := range summaries {
				check(s.Href)
			}
		}
	}
}
