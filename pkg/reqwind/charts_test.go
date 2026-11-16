package reqwind

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reqwind/assets"
)

func TestSharedAssetsWriteOnlyWhenChanged(t *testing.T) {
	dir := t.TempDir()
	index := filepath.Join(dir, "index.html")
	write := func() {
		t.Helper()
		if err := writeSharedAssets(index); err != nil {
			t.Fatal(err)
		}
	}
	write()
	old := time.Unix(1000000000, 0)
	for _, name := range []string{echartsAssetName, mapAssetName} {
		path := filepath.Join(dir, name)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	write()
	for _, name := range []string{echartsAssetName, mapAssetName} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !info.ModTime().Equal(old) {
			t.Fatalf("unchanged asset %s was rewritten", name)
		}
	}
	writeTest(t, filepath.Join(dir, echartsAssetName), "outdated")
	if err := os.Remove(filepath.Join(dir, mapAssetName)); err != nil {
		t.Fatal(err)
	}
	write()
	for name, want := range map[string][]byte{echartsAssetName: assets.EChartsJS, mapAssetName: assets.WorldGeoJSON} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("asset %s was not restored", name)
		}
	}
}

func TestChartCombinedAndHours(t *testing.T) {
	days := []Day{{
		Date: "2026-11-01", Requests: 3, Bytes: 2048, Files: 2, Pages: 1, Visits: 1,
		Paths:    map[string]int64{"/": 2, "/a": 1},
		Visitors: map[string]bool{"v1": true, "v2": true},
		Hours:    [24]int64{1, 1},
	}, {
		Date: "2026-11-08", Requests: 4, Bytes: 100, Files: 1, Pages: 1, Visits: 2,
		Paths:    map[string]int64{"/": 4},
		Visitors: map[string]bool{"v2": true},
		Hours:    [24]int64{2},
	}}
	// Monthly: the two days fall in the same month, so a single period carries
	// the summed totals.
	d := extractChartData(t, chartHTML(days, true))
	if len(d.Labels) != 1 || d.Labels[0] != "2026-11" {
		t.Fatalf("monthly labels: %v", d.Labels)
	}
	// Visitors is the distinct union across the two days: {v1,v2} ∪ {v2} = 2.
	if d.Requests[0] != 7 || d.Visitors[0] != 2 || d.Files[0] != 3 || d.Pages[0] != 2 || d.Visits[0] != 3 || d.Bytes[0] != 2148 {
		t.Fatalf("monthly combined totals wrong: %+v", d)
	}
	var hourTotal int64
	for _, h := range d.Hours {
		hourTotal += h
	}
	if hourTotal != 4 || !d.HasHours {
		t.Fatalf("hours should sum to 4 and be present: %+v", d)
	}
	// Daily: two periods.
	dd := extractChartData(t, chartHTML(days, false))
	if len(dd.Labels) != 2 || dd.Labels[0] != "2026-11-01" || dd.Labels[1] != "2026-11-08" {
		t.Fatalf("daily labels: %v", dd.Labels)
	}
}

func TestCountryMapVisitorMode(t *testing.T) {
	days := []Day{{
		Date:      "2026-11-01",
		Requests:  10,
		Countries: map[string]int64{"US": 10, "DE": 5},
		CountryVisitors: map[string]map[string]bool{
			"US": {"a": true, "b": true},
			"DE": {"a": true, "c": true},
		},
	}, {
		Date:            "2026-11-02",
		Requests:        10,
		Countries:       map[string]int64{"US": 10, "DE": 5},
		CountryVisitors: map[string]map[string]bool{"US": {"a": true}, "DE": {"a": true}},
	}}
	out := chartHTML(days, false)
	// Map must show unique visitor counts, not the request counts:
	// US {a,b} -> 2, DE {a,c} -> 2 (union across days).
	d := extractChartData(t, out)
	vals := map[string]int64{}
	for _, mv := range d.MapValues {
		vals[mv.Name] = mv.Value
	}
	if vals["United States"] != 2 || vals["Germany"] != 2 {
		t.Fatalf("visitor union wrong: %v", vals)
	}
	// The fallback request counts (10,5) must not appear as map values.
	for _, mv := range d.MapValues {
		if mv.Value == 10 || mv.Value == 5 {
			t.Fatalf("map leaked request counts: %v", d.MapValues)
		}
	}
	// The map table defaults to visitor order and is header-click sortable via
	// the shared report-summary script.
	if !strings.Contains(string(out), `<table class="report-summary">`) {
		t.Fatal("map table must carry the report-summary sortable class")
	}
}

func TestMapTableDefaultsToVisitorOrder(t *testing.T) {
	days := []Day{{
		Date: "2026-11-01", Requests: 5,
		CountryVisitors: map[string]map[string]bool{
			"US": {"a": true, "b": true, "c": true},
			"DE": {"a": true},
		},
	}}
	s := string(chartHTML(days, false))
	// The table row (not the JSON payload) must list US before DE.
	us := strings.Index(s, `>United States</th>`)
	de := strings.Index(s, `>Germany</th>`)
	if us < 0 || de < 0 || us > de {
		t.Fatalf("map table must list visitors by count: US=%d DE=%d", us, de)
	}
}

func TestCountryMapOlderState(t *testing.T) {
	// Older state: request counts exist, but no per-country visitor sets.
	days := []Day{{
		Date:      "2026-11-01",
		Requests:  10,
		Countries: map[string]int64{"US": 7, "DE": 3},
	}}
	out := chartHTML(days, false)
	d := extractChartData(t, out)
	if len(d.MapValues) != 0 {
		t.Fatalf("request counts must not be shown as visitors: %v", d.MapValues)
	}
	if !strings.Contains(string(out), "replay older logs into a new state file") {
		t.Error("old state must carry a plain-text caveat")
	}
}

func TestCountryMapInjectionSafe(t *testing.T) {
	// A malicious country code that is not in the geometry table must be
	// dropped, and a hostile name must never reach the map payload.
	days := []Day{{
		Date:            "2026-11-01",
		Requests:        1,
		CountryVisitors: map[string]map[string]bool{"</script>": {"a": true}, "ZZ": {"a": true}, "US": {"a": true, "b": true}},
	}}
	out := chartHTML(days, false)
	s := string(out)
	if strings.Contains(s, `</script></script>`) {
		t.Fatal("country code escaped out of the script block")
	}
	d := extractChartData(t, out)
	vals := map[string]int64{}
	for _, mv := range d.MapValues {
		vals[mv.Name] = mv.Value
	}
	if vals["United States"] != 2 {
		t.Fatalf("valid country missing: %v", vals)
	}
	for name := range vals {
		if name != "United States" {
			t.Fatalf("unknown country %q reached the map", name)
		}
	}
}

func TestMapTableShowsNonGeoBuckets(t *testing.T) {
	// Non-geo buckets (private-range and unresolvable) have no world-map
	// polygon: they must still count in the table but stay off the map.
	days := []Day{{
		Date:            "2026-11-01",
		Requests:        5,
		CountryVisitors: map[string]map[string]bool{"US": {"a": true}, localPrivate: {"b": true, "c": true}, unknownCountry: {"d": true}},
	}}
	out := chartHTML(days, false)
	for _, row := range []string{`>Local/private</th>`, `>Unknown</th>`, `>2</td>`, `>1</td>`} {
		if !strings.Contains(string(out), row) {
			t.Fatalf("map table missing non-geo row %s", row)
		}
	}
	d := extractChartData(t, out)
	if len(d.MapValues) != 1 || d.MapValues[0].Name != "United States" {
		t.Fatalf("only real countries reach the map: %v", d.MapValues)
	}
	other := map[string]int64{}
	for _, v := range d.OtherValues {
		other[v.Name] = v.Value
	}
	if other[localPrivate] != 2 || other[unknownCountry] != 1 || len(other) != 2 {
		t.Fatalf("non-geo buckets not counted separately: %v", d.OtherValues)
	}
}

func TestChartEmpty(t *testing.T) {
	out := chartHTML(nil, false)
	if !strings.Contains(string(out), `id="country-map"`) {
		t.Fatal("empty chart must still render the map anchor")
	}
	if !strings.Contains(string(out), "No data.") {
		t.Fatal("empty chart must render the no-data placeholder")
	}
}

func TestChartOSAndStatusPies(t *testing.T) {
	days := []Day{{
		Date: "2026-11-01",
		Agents: map[string]int64{
			"Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/120.0":            5,
			"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) Safari/605": 2,
			"Mozilla/5.0 (iPad; CPU OS 17_0 like Mac OS X) Safari/605":          1,
			"curl/8.0": 1,
			"":         3,
			// A crawler that copies a browser user agent must not count as Windows.
			"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36 (compatible; meta-externalagent/1.1 (+https://developers.facebook.com/docs/sharing/webmasters/crawler))": 4,
			"Googlebot/2.1 (+http://www.google.com/bot.html)": 2,
		},
		Status: map[string]int64{"101": 2, "200": 4, "301": 1, "404": 2, "500": 1, "999": 1},
	}}
	d := extractChartData(t, chartHTML(days, false))
	osVals := map[string]int64{}
	for _, v := range d.OSValues {
		osVals[v.Name] = v.Value
	}
	if osVals["windows"] != 5 || osVals["iphone"] != 2 || osVals["ipad"] != 1 || osVals["curl"] != 1 || osVals["Unknown"] != 3 || osVals["Crawler"] != 6 {
		t.Fatalf("OS buckets wrong: %v", osVals)
	}
	statusVals := map[string]int64{}
	for _, v := range d.Statuses {
		statusVals[v.Name] = v.Value
	}
	// Ranges keep the fixed HTTP order 2xx, 3xx, 4xx, 5xx, Other.
	if statusVals["2xx"] != 4 || statusVals["3xx"] != 1 || statusVals["4xx"] != 2 || statusVals["5xx"] != 1 || statusVals["Other"] != 3 {
		t.Fatalf("status ranges wrong: %v", statusVals)
	}
	order := []string{}
	for _, v := range d.Statuses {
		order = append(order, v.Name)
	}
	if want := []string{"2xx", "3xx", "4xx", "5xx", "Other"}; len(order) != 5 {
		t.Fatalf("status slice order wrong: %v", order)
	} else {
		for i, name := range want {
			if order[i] != name {
				t.Fatalf("status slice order wrong: %v", order)
			}
		}
	}
}

func TestVhostChartsOnlyWithMultipleHosts(t *testing.T) {
	multi := []Day{
		{Date: "2026-11-01", Vhost: "one.example", Requests: 3, Bytes: 100},
		{Date: "2026-11-01", Vhost: "two.example", Requests: 2, Bytes: 50},
		{Date: "2026-11-02", Vhost: "two.example", Requests: 4, Bytes: 60},
	}
	out := string(chartHTML(multi, false))
	for _, want := range []string{`id="vhost-requests-chart"`, `id="vhost-traffic-chart"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("multi-host page missing %s", want)
		}
	}
	d := extractChartData(t, chartHTML(multi, false))
	if len(d.VhostSeries) != 2 {
		t.Fatalf("want 2 vhost series: %+v", d.VhostSeries)
	}
	// Series are sorted by name and aligned to the labels.
	if d.VhostSeries[0].Name != "one.example" || d.VhostSeries[1].Name != "two.example" {
		t.Fatalf("vhost series order wrong: %+v", d.VhostSeries)
	}
	if d.VhostSeries[0].Requests[0] != 3 || d.VhostSeries[0].Bytes[0] != 100 {
		t.Fatalf("one.example period totals wrong: %+v", d.VhostSeries[0])
	}
	if d.VhostSeries[1].Requests[0] != 2 || d.VhostSeries[1].Requests[1] != 4 || d.VhostSeries[1].Bytes[1] != 60 {
		t.Fatalf("two.example period totals wrong: %+v", d.VhostSeries[1])
	}
	// A single-host page keeps no by-vhost sections and no series.
	single := string(chartHTML([]Day{{Date: "2026-11-01", Vhost: "one.example", Requests: 3}}, false))
	for _, banned := range []string{`id="vhost-requests-chart"`, `id="vhost-traffic-chart"`} {
		if strings.Contains(single, banned) {
			t.Fatalf("single-host page must not render %s", banned)
		}
	}
	if d := extractChartData(t, chartHTML([]Day{{Date: "2026-11-01", Vhost: "one.example", Requests: 3}}, false)); len(d.VhostSeries) != 0 {
		t.Fatalf("single-host page must not carry vhost series: %+v", d.VhostSeries)
	}
}

func TestChartTablesCollapsed(t *testing.T) {
	out := string(chartHTML([]Day{{Date: "2026-11-01", Requests: 5, Bytes: 2048, Visits: 3,
		Hours: [24]int64{5}, CountryVisitors: map[string]map[string]bool{"US": {"a": true}},
	}}, false))
	for _, id := range []string{"activity-chart", "traffic-chart", "visits-chart", "hours-chart", "country-map"} {
		start := strings.Index(out, `id="`+id+`"`)
		section := out[start : start+strings.Index(out[start:], `</section>`)]
		if !strings.Contains(section, `<details><summary>`) || !strings.Contains(section, `<table`) || strings.Contains(section, `<details open`) {
			t.Errorf("%s needs a collapsed table", id)
		}
	}
	for _, want := range []string{`>2.0 KB</td>`, `>3</td>`, `<summary>Hourly requests</summary>`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing table value %s", want)
		}
	}
}

func TestIndexOmitsPerDayTotalsTable(t *testing.T) {
	days := []Day{{Date: "2026-11-01", Requests: 5, Bytes: 2048, Visits: 3}}
	// A detail page keeps the per-day totals table; the index drops it.
	if out := chartHTML(days, false); !strings.Contains(string(out), `<summary>Per-day totals</summary>`) {
		t.Fatal("detail page must keep the per-day totals table")
	}
	if out := chartHTMLFull(days, false, false, true); strings.Contains(string(out), `<summary>Per-day totals</summary>`) {
		t.Fatal("index must omit the per-day totals table")
	}
	// The chart itself is still present on the index.
	if out := chartHTMLFull(days, false, false, true); !strings.Contains(string(out), `id="rw-chart-activity"`) {
		t.Fatal("index must still render the activity chart")
	}
}

func TestYearlyReportOmitsPerDayTotalsTable(t *testing.T) {
	// All-sites yearly reports omit the 365-row per-day totals table; index
	// and month pages keep it (index via its flag, months keep the rows).
	days := []Day{{Date: "2026-11-01", Requests: 1}, {Date: "2026-11-02", Requests: 2}}
	r := Report{Days: days}
	pages := BuildPages("/reports/index.html", r)
	var foundYear bool
	for _, p := range pages {
		if p.Path == "/reports/index-year-2026.html" {
			foundYear = true
			if strings.Contains(mustRender(t, p.Report), `<summary>Per-day totals</summary>`) {
				t.Fatal("all-sites yearly report must omit the per-day totals table")
			}
			if !strings.Contains(mustRender(t, p.Report), `id="rw-chart-activity"`) {
				t.Fatal("all-sites yearly report must still render the activity chart")
			}
		}
	}
	if !foundYear {
		t.Fatal("all-sites yearly page missing from BuildPages")
	}
	// A month page keeps the per-day totals table.
	for _, p := range pages {
		if p.Path == "/reports/index-month-2026-11.html" {
			if !strings.Contains(mustRender(t, p.Report), `<summary>Per-day totals</summary>`) {
				t.Fatal("all-sites month page must keep the per-day totals table")
			}
		}
	}
}

func TestExternalEChartsReference(t *testing.T) {
	days := []Day{{Date: "2026-11-01", Requests: 1}}
	// Default embeds the whole library inline.
	if out := string(chartHTML(days, false)); strings.Contains(out, `<script src="echarts.min.js">`) {
		t.Fatal("default page must not reference an external library")
	}
	// External mode references the shared files and skips the inline copies.
	out := string(chartHTMLFull(days, false, true, false))
	if !strings.Contains(out, `<script src="echarts.min.js"></script>`) {
		t.Fatal("external page must reference echarts.min.js")
	}
	// The world-map geometry is also external: no inline rw-map-data script and
	// no embedded geometry in the page.
	if strings.Contains(out, `id="rw-map-data"`) {
		t.Fatal("external page must not embed rw-map-data")
	}
	if strings.Contains(out, "FeatureCollection") {
		t.Fatal("external page must not embed the world-map geometry")
	}
	// The init script still follows the library on every page.
	if !strings.Contains(out, chartInitJS) {
		t.Fatal("chart init script missing")
	}
	// Inline mode still embeds both.
	if in := string(chartHTML(days, false)); !strings.Contains(in, `id="rw-map-data"`) || !strings.Contains(in, "FeatureCollection") {
		t.Fatal("inline page must embed the world-map geometry")
	}
}

func TestChartEmbeddedAssets(t *testing.T) {
	out := chartHTML([]Day{{Date: "2026-11-01", Requests: 1}}, false)
	s := string(out)
	// ECharts library and world geometry are embedded inline (no network).
	if !strings.Contains(s, "Apache License") || !strings.Contains(s, "FeatureCollection") {
		t.Fatal("bundled ECharts / world geometry missing from output")
	}
	// No remote resource is referenced.
	for _, banned := range []string{`src="http`, `src="//`, `href="http`} {
		if strings.Contains(s, banned) {
			t.Errorf("chart output references remote resource %q", banned)
		}
	}
}

func TestChartStableMapAndGeometry(t *testing.T) {
	days := []Day{{Date: "</script>x", CountryVisitors: map[string]map[string]bool{"US": {"a": true}, "DE": {"b": true}}}}
	want := chartHTML(days, false)
	for i := 0; i < 8; i++ {
		if got := chartHTML(days, false); got != want {
			t.Fatal("chart output changed without new data")
		}
	}
	if strings.Contains(chartJSONRaw(t, want), "</script>") {
		t.Fatal("unescaped chart label")
	}
	var geometry struct {
		Features []struct {
			Properties struct {
				Name string `json:"name"`
			} `json:"properties"`
		} `json:"features"`
	}
	if err := json.Unmarshal(assets.WorldGeoJSON, &geometry); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, feature := range geometry.Features {
		names[feature.Properties.Name] = true
	}
	for code, name := range countryFeature {
		if !names[name] {
			t.Errorf("%s maps to nonexistent polygon %q", code, name)
		}
	}
}

func TestHourLabelsUseDisplayTimezone(t *testing.T) {
	withLocal(t, "Asia/Kolkata", func() {
		d := extractChartData(t, chartHTML([]Day{{Date: "2026-11-12", Hours: [24]int64{3}}}, false))
		counts := map[string]int64{}
		for i, label := range d.HourLabels {
			counts[label] = d.Hours[i]
		}
		if counts["05:30"] != 3 || len(counts) != 24 {
			t.Fatalf("UTC-hour starts were not converted to local time: %v", counts)
		}
	})
	withLocal(t, "America/New_York", func() {
		d := extractChartData(t, chartHTML([]Day{{Date: "2026-07-01", Hours: [24]int64{2}}, {Date: "2026-12-01", Hours: [24]int64{3}}}, false))
		counts := map[string]int64{}
		for i, label := range d.HourLabels {
			counts[label] = d.Hours[i]
		}
		if counts["20:00"] != 2 || counts["19:00"] != 3 {
			t.Fatalf("historical DST offsets were lost: %v", counts)
		}
	})
}

func chartJSONRaw(t *testing.T, html template.HTML) string {
	t.Helper()
	s := string(html)
	start := strings.Index(s, `<script id="rw-chart-data" type="application/json">`)
	if start < 0 {
		t.Fatal("chart data script missing")
	}
	start += len(`<script id="rw-chart-data" type="application/json">`)
	end := strings.Index(s[start:], `</script>`)
	if end < 0 {
		t.Fatal("chart data script unterminated")
	}
	return s[start : start+end]
}

func extractChartData(t *testing.T, html template.HTML) chartData {
	t.Helper()
	var d chartData
	if err := json.Unmarshal([]byte(chartJSONRaw(t, html)), &d); err != nil {
		t.Fatalf("chart data is not valid JSON: %v", err)
	}
	return d
}

func BenchmarkChartPathCardinality(b *testing.B) {
	for _, n := range []int{1, 10000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			days := []Day{{Date: "2026-11-01", Requests: 1, Visitors: map[string]bool{"visitor": true}, Paths: map[string]int64{}}}
			for i := 0; i < n; i++ {
				days[0].Paths[fmt.Sprintf("/path/%d", i)] = 1
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				chartHTMLFull(days, false, true, true)
			}
		})
	}
}
