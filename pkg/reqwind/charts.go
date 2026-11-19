package reqwind

import (
	_ "embed"
	"encoding/json"
	"html/template"
	"maps"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"reqwind/assets"
)

// chartPeriod is one aggregated period (day or month) for the combined charts.
type chartPeriod struct {
	Label    string
	Requests int64
	Visitors int64
	Files    int64
	Pages    int64
	Visits   int64
	Bytes    int64
}

// mapValue is one country's value on the world map.
type mapValue struct {
	Name  string `json:"name"`
	Value int64  `json:"value"`
}

// pieValue is one slice of the operating-system and status-code pie charts.
type pieValue struct {
	Name  string `json:"name"`
	Value int64  `json:"value"`
}

// vhostSeries is one host's per-period request and byte lines for the
// by-vhost charts. Values are zero where the host has no traffic in a period.
type vhostSeries struct {
	Name     string  `json:"name"`
	Requests []int64 `json:"requests"`
	Bytes    []int64 `json:"bytes"`
}

// chartData is the exact JSON payload embedded into the report; the JS side
// reads only these fields.
type chartData struct {
	Unit       string     `json:"unit"`
	Labels     []string   `json:"labels"`
	Requests   []int64    `json:"requests"`
	Visitors   []int64    `json:"visitors"`
	Files      []int64    `json:"files"`
	Pages      []int64    `json:"pages"`
	Visits     []int64    `json:"visits"`
	Bytes      []int64    `json:"bytes"`
	HasDays    bool       `json:"hasDays"`
	Hours      []int64    `json:"hours"`
	HourLabels []string   `json:"hourLabels"`
	HasHours   bool       `json:"hasHours"`
	MapValues  []mapValue `json:"map"`
	OSValues   []pieValue `json:"os"`
	Statuses   []pieValue `json:"statuses"`
	// MapExternal tells the JS to fetch the world-map geometry from the shared
	// world.geo.json file instead of an embedded script tag.
	MapExternal bool `json:"mapExternal"`
	// OtherValues holds non-geo buckets (private-range and unresolvable
	// visitors) that have no world-map polygon. They show in the totals table
	// but not on the map.
	OtherValues []mapValue `json:"otherValues"`
	// VhostSeries holds one per-vhost line for the by-vhost request and
	// traffic charts.
	VhostSeries []vhostSeries `json:"vhosts"`
}

// unknownCountry is the bucket for visitors geo could not place.
const unknownCountry = "Unknown"

// chartHTML renders the self-contained interactive charts (combined activity,
// traffic, visits, hours and the world map). The ECharts library, the world
// map geometry and the chart data are all embedded, so no network access is
// needed. JSON is produced with encoding/json, which escapes <, > and & so
// map keys cannot break out of the <script> element. omitDayTable hides the
// per-day totals table (index and all-sites yearly reports).
// Shared asset files written once beside the report index when the
// external-echarts flag is set, so pages reference them instead of embedding
// the ~1 MB chart library and world-map geometry in every page.
const (
	echartsAssetName = "echarts.min.js"
	mapAssetName     = "world.geo.json"
)

// writeSharedAssets writes missing or changed assets beside the report index.
func writeSharedAssets(index string) error {
	dir := filepath.Dir(index)
	for _, asset := range []struct {
		name string
		data []byte
	}{{echartsAssetName, assets.EChartsJS}, {mapAssetName, assets.WorldGeoJSON}} {
		path := filepath.Join(dir, asset.name)
		if err := writeIfChanged(path, asset.data, 0644); err != nil {
			return err
		}
	}
	return nil
}

// chartHTML renders the self-contained charts, embedding the whole ECharts
// library and world-map geometry inline so each page is fully self-contained.
// chartHTMLFull controls whether the assets are shared files instead.
func chartHTML(days []Day, monthly bool) template.HTML {
	return chartHTMLFull(days, monthly, false, false, false)
}

func chartHTMLFull(days []Day, monthly, externalECharts, omitDayTable, omitPeriodCharts bool) template.HTML {
	prefix := 10
	unit := "day"
	if monthly {
		prefix = 7
		unit = "month"
	}
	chartDays := map[string]*Day{}

	var periods []chartPeriod
	hours := map[string]int64{}
	var hourTotal int64
	// Unique visitor hash sets per country; the hash is a stable HMAC of the
	// client address, so a union across days is the true distinct count.
	countrySets := map[string]map[string]struct{}{}
	for _, d := range days {
		if date, err := time.Parse("2006-01-02", d.Date); err == nil {
			for hour, count := range d.Hours {
				label := date.Add(time.Duration(hour) * time.Hour).In(time.Local).Format("15:04")
				hours[label] += count
				hourTotal += count
			}
		}
		for c, vs := range d.CountryVisitors {
			set := countrySets[c]
			if set == nil {
				set = map[string]struct{}{}
				countrySets[c] = set
			}
			for h := range vs {
				set[h] = struct{}{}
			}
		}
		label := d.Date
		if len(label) > prefix {
			label = label[:prefix]
		}
		period := chartDays[label]
		if period == nil {
			period = &Day{Date: label, Visitors: map[string]bool{}}
			chartDays[label] = period
		}
		period.Requests += d.Requests
		period.Files += d.Files
		period.Pages += d.Pages
		period.Visits += d.Visits
		period.Bytes += d.Bytes
		for id := range d.Visitors {
			period.Visitors[id] = true
		}
	}
	for _, label := range slices.Sorted(maps.Keys(chartDays)) {
		d := chartDays[label]
		periods = append(periods, chartPeriod{
			Label:    d.Date,
			Requests: d.Requests,
			Visitors: int64(len(d.Visitors)),
			Files:    d.Files,
			Pages:    d.Pages,
			Visits:   d.Visits,
			Bytes:    d.Bytes,
		})
	}

	data := chartData{Unit: unit, HasDays: len(chartDays) > 0, HasHours: hourTotal > 0}
	for label := range hours {
		data.HourLabels = append(data.HourLabels, label)
	}
	sort.Strings(data.HourLabels)
	for _, label := range data.HourLabels {
		data.Hours = append(data.Hours, hours[label])
	}
	for _, p := range periods {
		data.Labels = append(data.Labels, p.Label)
		data.Requests = append(data.Requests, p.Requests)
		data.Visitors = append(data.Visitors, p.Visitors)
		data.Files = append(data.Files, p.Files)
		data.Pages = append(data.Pages, p.Pages)
		data.Visits = append(data.Visits, p.Visits)
		data.Bytes = append(data.Bytes, p.Bytes)
	}
	// By-vhost series: one line per host, aligned to data.Labels. Only pages
	// with more than one host get them, so single-site vhost pages stay clean.
	if vhosts := buildVhostSeries(days, data.Labels); len(vhosts) > 1 {
		data.VhostSeries = vhosts
	}
	for code, set := range countrySets {
		if len(set) == 0 {
			continue
		}
		name := countryFeature[code]
		if name == "" {
			// No world-map polygon (private-range, or unresolvable). Keep it in
			// the totals table, not on the map.
			data.OtherValues = append(data.OtherValues, mapValue{Name: code, Value: int64(len(set))})
			continue
		}
		data.MapValues = append(data.MapValues, mapValue{Name: name, Value: int64(len(set))})
	}
	// The table and map default to visitor order; name breaks ties.
	sort.Slice(data.OtherValues, func(i, j int) bool { return data.OtherValues[i].Name < data.OtherValues[j].Name })
	sort.Slice(data.MapValues, func(i, j int) bool {
		if data.MapValues[i].Value != data.MapValues[j].Value {
			return data.MapValues[i].Value > data.MapValues[j].Value
		}
		return data.MapValues[i].Name < data.MapValues[j].Name
	})

	osCounts, statusCounts := map[string]int64{}, map[string]int64{}
	for _, d := range days {
		for agent, count := range d.Agents {
			osCounts[classifyOS(agent)] += count
		}
		for code, count := range d.Status {
			statusCounts[statusClass(code)] += count
		}
	}
	for _, v := range topN(osCounts, len(osCounts)) {
		data.OSValues = append(data.OSValues, pieValue{Name: v.Name, Value: v.Count})
	}
	// Fixed HTTP-range order keeps the status legend stable.
	for _, class := range []string{"2xx", "3xx", "4xx", "5xx", "Other"} {
		if count := statusCounts[class]; count > 0 {
			data.Statuses = append(data.Statuses, pieValue{Name: class, Value: count})
		}
	}
	data.MapExternal = externalECharts
	dataJSON, _ := json.Marshal(data) // plain numbers and validated strings; cannot fail
	// Inline mode embeds the world-map geometry in a script tag; external mode
	// omits it and the JS fetches the shared world.geo.json instead.
	geoJSON := []byte{}
	if !externalECharts {
		geoJSON = assets.WorldGeoJSON
		if hasScriptBreak(geoJSON) {
			// The payload must never contain the sequence that ends a script tag.
			return template.HTML("")
		}
	}
	if hasScriptBreak(dataJSON) {
		return template.HTML("")
	}

	parts := []string{}
	// A per-day page has nothing to trend; skip the per-period charts and
	// keep the rest of the page. Month pages keep them even with one day.
	if !omitPeriodCharts {
		parts = append(parts,
			`<section id="activity-chart" class="summary" aria-label="Activity chart">`,
			`<h2>Requests / visitors / files / pages per `+unit+`</h2>`,
			activityBody(unit, periods, &data, omitDayTable),
			`</section>`)
		// By-vhost sections appear only when the page holds more than one
		// host; a single-host page would only repeat the combined charts.
		if len(data.VhostSeries) > 1 {
			parts = append(parts,
				`<section id="vhost-requests-chart" class="summary" aria-label="Requests by virtual host">`,
				`<h2>Requests per `+unit+` by virtual host</h2>`,
				vhostBody(unit, &data, false),
				`</section>`,
				`<section id="vhost-traffic-chart" class="summary" aria-label="Traffic by virtual host">`,
				`<h2>Traffic per `+unit+` by virtual host</h2>`,
				vhostBody(unit, &data, true),
				`</section>`)
		}
		parts = append(parts,
			`<section id="traffic-chart" class="summary" aria-label="Traffic chart">`,
			`<h2>Traffic per `+unit+`</h2>`,
			chartDiv("rw-chart-traffic", "Traffic per "+unit+" in bytes"),
			periodTable(unit, periods, true),
			`</section>`,
			`<section id="visits-chart" class="summary" aria-label="Visits chart">`,
			`<h2>Visits per `+unit+`</h2>`,
			chartDiv("rw-chart-visits", "Visits per "+unit),
			periodTable(unit, periods, false),
			`</section>`)
	}
	parts = append(parts,
		`<section id="hours-chart" class="summary" aria-label="Hours chart">`,
		`<h2>Requests by hour of day (local time, all days)</h2>`,
		`<p class="meta">UTC-hour intervals grouped by their local start time.</p>`,
		hoursBody(&data),
		`</section>`,
		`<section id="country-map" class="summary" aria-label="World map">`,
		`<h2>Visitors by country</h2>`,
		mapBody(&data),
		`</section>`,
		`<script id="rw-chart-data" type="application/json">`+string(dataJSON)+`</script>`)
	if externalECharts {
		parts = append(parts,
			`<script src="`+echartsAssetName+`"></script>`,
			`<script>`+chartInitJS+`</script>`)
	} else {
		parts = append(parts,
			`<script id="rw-map-data" type="application/json">`+string(geoJSON)+`</script>`,
			`<script>`+string(assets.EChartsJS)+`</script>`,
			`<script>`+chartInitJS+`</script>`)
	}
	return template.HTML(strings.Join(parts, ""))
}

// activityBody renders the combined chart plus, unless omitTable, the plain
// per-period table (a no-JS data fallback).
func activityBody(unit string, periods []chartPeriod, data *chartData, omitTable bool) string {
	if !data.HasDays {
		return `<p class="empty">No data.</p>`
	}
	var b strings.Builder
	b.WriteString(chartDiv("rw-chart-activity", "Requests, visitors, files and pages per "+unit))
	if !omitTable {
		b.WriteString(`<details><summary>Per-` + unit + ` totals</summary><table><caption>Per-` + unit + ` totals (also shown in the chart above)</caption>`)
		b.WriteString(`<thead><tr><th scope="col">` + unit + `</th><th scope="col" class="num">requests</th><th scope="col" class="num">visitors</th><th scope="col" class="num">files</th><th scope="col" class="num">pages</th><th scope="col" class="num">visits</th><th scope="col" class="num">bytes</th></tr></thead><tbody>`)
		for _, p := range periods {
			b.WriteString("\n" + `<tr><td>` + template.HTMLEscapeString(p.Label) + `</td><td class="num">` + group(p.Requests) + `</td><td class="num">` + group(p.Visitors) + `</td><td class="num">` + group(p.Files) + `</td><td class="num">` + group(p.Pages) + `</td><td class="num">` + group(p.Visits) + `</td><td class="num" title="` + group(p.Bytes) + ` B">` + humanBytes(p.Bytes) + `</td></tr>`)
		}
		b.WriteString(`</tbody></table></details>`)
	}
	return b.String()
}

func periodTable(unit string, periods []chartPeriod, traffic bool) string {
	if len(periods) == 0 {
		return ""
	}
	metric := "visits"
	if traffic {
		metric = "traffic"
	}
	var b strings.Builder
	b.WriteString(`<details><summary>Per-` + unit + ` ` + metric + `</summary><table><thead><tr><th scope="col">` + unit + `</th><th scope="col" class="num">` + metric + `</th></tr></thead><tbody>`)
	for _, p := range periods {
		value := group(p.Visits)
		if traffic {
			value = humanBytes(p.Bytes)
		}
		b.WriteString("\n" + `<tr><th scope="row">` + template.HTMLEscapeString(p.Label) + `</th><td class="num">` + value + `</td></tr>`)
	}
	b.WriteString(`</tbody></table></details>`)
	return b.String()
}

// distinctHosts counts the vhost groups in a day set; empty vhosts form the
// "(unknown)" group.
func distinctHosts(days []Day) int {
	seen := map[string]bool{}
	for _, d := range days {
		host := d.Vhost
		if host == "" {
			host = "(unknown)"
		}
		seen[host] = true
	}
	return len(seen)
}

// buildVhostSeries groups one day set into per-host request and byte series,
// aligned to the given labels. Hosts keep the vhost summary table's display
// names and are sorted so the legend stays stable.
func buildVhostSeries(days []Day, labels []string) []vhostSeries {
	byHost := map[string][]Day{}
	for _, d := range days {
		host := d.Vhost
		if host == "" {
			host = "(unknown)"
		}
		byHost[host] = append(byHost[host], d)
	}
	index := make(map[string]int, len(labels))
	for i, label := range labels {
		index[label] = i
	}
	type acc struct {
		requests, bytes []int64
	}
	series := map[string]*acc{}
	// Same naming rule as the vhost summary table; raw hosts are unique, so
	// no two groups can share a name.
	nameOf := func(host string) string {
		name := inferSiteName(host, byHost[host])
		if name == "" {
			name = host
		}
		return name
	}
	for _, host := range sortedDayKeys(byHost, false) {
		a := &acc{requests: make([]int64, len(labels)), bytes: make([]int64, len(labels))}
		series[nameOf(host)] = a
		for _, d := range byHost[host] {
			label := d.Date
			// The chart periods bucket days by their first 7 or 10 characters;
			// match that width, not the count of periods.
			if len(labels) > 0 && len(label) > len(labels[0]) {
				label = label[:len(labels[0])]
			}
			i, ok := index[label]
			if !ok {
				continue
			}
			a.requests[i] += d.Requests
			a.bytes[i] += d.Bytes
		}
	}
	names := make([]string, 0, len(series))
	for name := range series {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]vhostSeries, 0, len(names))
	for _, name := range names {
		a := series[name]
		out = append(out, vhostSeries{Name: name, Requests: a.requests, Bytes: a.bytes})
	}
	return out
}

func chartDiv(id, label string) string {
	return `<div id="` + id + `" class="rw-chart" style="height:320px" role="img" aria-label="` + label + `"></div>`
}

// vhostBody renders one by-vhost chart section body. traffic picks the byte
// series; the chart legend names the hosts, so no table fallback is needed.
func vhostBody(unit string, data *chartData, traffic bool) string {
	if !data.HasDays || len(data.VhostSeries) == 0 {
		return `<p class="empty">No data.</p>`
	}
	label := "Traffic per " + unit + " by virtual host in bytes"
	if !traffic {
		label = "Requests per " + unit + " by virtual host"
	}
	return chartDiv("rw-chart-vhost-"+map[bool]string{true: "traffic", false: "requests"}[traffic], label)
}

func hoursBody(data *chartData) string {
	if !data.HasHours {
		return `<p class="empty">No data.</p>`
	}
	var b strings.Builder
	b.WriteString(chartDiv("rw-chart-hours", "Requests by hour of day"))
	b.WriteString(`<details><summary>Hourly requests</summary><table><thead><tr><th scope="col">local hour</th><th scope="col" class="num">requests</th></tr></thead><tbody>`)
	for i, label := range data.HourLabels {
		b.WriteString("\n" + `<tr><th scope="row">` + template.HTMLEscapeString(label) + `</th><td class="num">` + group(data.Hours[i]) + `</td></tr>`)
	}
	b.WriteString(`</tbody></table></details>`)
	return b.String()
}

func mapBody(data *chartData) string {
	if len(data.MapValues) == 0 && len(data.OtherValues) == 0 {
		return `<p class="empty">No stored country visitor data. Enable local GeoIP; replay older logs into a new state file to include history.</p>`
	}
	var b strings.Builder
	if len(data.MapValues) > 0 {
		b.WriteString(chartDiv("rw-chart-map", "World map of unique visitors by country"))
	}
	// class="report-summary" reuses the shared header-click sorting script.
	b.WriteString(`<details><summary>Mapped visitor totals</summary><table class="report-summary"><thead><tr><th scope="col">country</th><th scope="col" class="num">visitors</th></tr></thead><tbody>`)
	for _, country := range data.MapValues {
		b.WriteString("\n" + `<tr><th scope="row">` + template.HTMLEscapeString(country.Name) + `</th><td class="num" title="` + group(country.Value) + `">` + group(country.Value) + `</td></tr>`)
	}
	// Non-geo buckets (private-range, unresolvable) after the real countries.
	for _, country := range data.OtherValues {
		b.WriteString("\n" + `<tr><th scope="row">` + template.HTMLEscapeString(country.Name) + `</th><td class="num" title="` + group(country.Value) + `">` + group(country.Value) + `</td></tr>`)
	}
	b.WriteString(`</tbody></table></details>`)
	return b.String()
}

func hasScriptBreak(b []byte) bool {
	return strings.Contains(strings.ToLower(string(b)), "</script")
}

// chartInitJS reads the embedded JSON payloads, registers the world map and
// draws every chart. It is trusted static code, embedded from charts.js so
// it stays editable as a plain file.
//
//go:embed charts.js
var chartInitJS string
