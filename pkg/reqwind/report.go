package reqwind

import (
	_ "embed"
	"encoding/base64"
	"html/template"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"reqwind/assets"
)

// Report is the aggregated traffic report rendered by RenderReport.
type Report struct {
	Generated    time.Time
	Days         []Day
	InvalidLines int64
	// Live enables a <meta http-equiv="refresh" content="5"> tag.
	Live                  bool
	Title                 string
	IndexHref             string
	MonthlyCharts         bool
	Years, Months, Vhosts []Summary
	VhostSummary          []Summary
	// ShowDrilldown renders the prefix drilldown section. The all-sites
	// index leaves it false because the cross-host tree is too large.
	ShowDrilldown bool
	// ChartDays, when non-nil, limits this page's charts and path traffic table.
	// The all-sites index uses the trailing 12 months for all Days.
	ChartDays []Day
	// OmitDayTable hides the per-day totals table. All-sites yearly reports
	// set it: with up to 365 rows the table adds little next to the chart.
	OmitDayTable bool
	// Palette is the default colour scheme persisted into data-palette;
	// core validates it to one of ocean, forest or ember.
	Palette    string
	GeoEnabled bool
	// ExternalECharts makes every page load the chart library from the
	// single echarts.min.js written beside the index, instead of embedding
	// the ~1 MB library in each page.
	ExternalECharts bool
}

// Day holds per-day aggregates. Map keys (paths, referrers, agents, status
// codes) and Visitor hashes are untrusted input and must stay escaped.
type Day struct {
	Date              string
	Vhost             string
	Requests          int64
	Bytes             int64
	Status            map[string]int64
	Paths             map[string]int64
	PathBytes         map[string]int64 `json:",omitempty"`
	ErrorPaths        map[string]int64 `json:",omitempty"`
	UnauthorizedPaths map[string]int64 `json:",omitempty"`
	// RedirectPaths counts 3xx responses per path label. Optional so older
	// state files without it stay loadable.
	RedirectPaths        map[string]int64 `json:",omitempty"`
	Referrers            map[string]int64
	Agents               map[string]int64
	Terms                map[string]int64
	Visitors             map[string]bool
	Hours                [24]int64
	Files, Pages, Visits int64
	Clients              map[string]ClientStats
	Countries            map[string]int64
	CountryVisitors      map[string]map[string]bool
}

type top struct {
	Name  string
	Count int64
}

type clientEntry struct {
	Name     string
	Requests int64
	Bytes    int64
	Raw      bool // Name is a raw IP, not a hash
}

type totals struct {
	Requests, Files, Pages, Visits int64
	Bytes, Visitors                int64
}

type reportData struct {
	Version           string
	Logo              template.URL
	InteractiveCharts template.HTML
	PathDrilldown     template.HTML
	Generated         time.Time
	Title, IndexHref  string
	// Period is the data span shown under the page title.
	Period                string
	Favicon               template.URL
	Years, Months, Vhosts []Summary
	VhostSummary          []Summary
	ShowDrilldown         bool
	Live                  bool
	InvalidLines          int64
	Days                  []Day // sorted by date
	T                     totals
	TopPaths              []top
	TopErrorPaths         []top
	TopPathsBytes         []top
	TopRefs               []top
	TopAgents             []top
	TopStatus             []top
	TopTerms              []top
	TopClientsRequests    []clientEntry
	TopClientsBytes       []clientEntry
	TopCountries          []top
	HasDays               bool
	// HasVhostCharts adds the by-vhost chart anchors to the jump list.
	HasVhostCharts                                                    bool
	AvgDenominator                                                    int64 // calendar days from first to last data date
	AvgRequests, AvgBytes, AvgFiles, AvgPages, AvgVisits, AvgVisitors float64
	Palette                                                           string // ocean, forest or ember (validated by core)
	GeoEnabled                                                        bool
}

func RenderReport(w io.Writer, report Report) error {
	return reportTmpl.Execute(w, buildData(report))
}

var reportTmpl = template.Must(template.New("report").Funcs(template.FuncMap{
	"human":        humanBytes,
	"averageBytes": humanAverageBytes,
	"byteRate":     humanByteRate,
	"perSecond":    func(v float64) float64 { return v / 86400 },
	"group":        group,
	"hasRaw": func(list []clientEntry) bool {
		for _, c := range list {
			if c.Raw {
				return true
			}
		}
		return false
	},
}).Parse(reportHTML))

func buildData(r Report) reportData {
	days := groupDays(r.Days, 10)
	// Charts and path traffic use the chart window; other summaries and the
	// day table keep the full data set.
	chartSource := r.Days
	if r.ChartDays != nil {
		chartSource = r.ChartDays
	}
	// The all-sites index has no link back to itself (IndexHref is empty).
	isIndex := r.IndexHref == ""
	omitDayTable := isIndex || r.OmitDayTable

	paths, agents, status, terms := map[string]int64{}, map[string]int64{}, map[string]int64{}, map[string]int64{}
	pathBytes := map[string]int64{}
	errorPaths := map[string]int64{}
	unauthorizedPaths := map[string]int64{}
	redirectPaths := map[string]int64{}
	refs := externalReferrers(r.Days)
	clients, countries := map[string]ClientStats{}, map[string]int64{}
	var t totals
	var dailyVisitors int
	visitors := map[string]struct{}{}
	for _, d := range days {
		dailyVisitors += len(d.Visitors)
		t.Requests += d.Requests
		t.Bytes += d.Bytes
		t.Files += d.Files
		t.Pages += d.Pages
		t.Visits += d.Visits
		for k, v := range d.Clients {
			c := clients[k]
			c.Requests += v.Requests
			c.Bytes += v.Bytes
			if c.Raw == "" {
				c.Raw = v.Raw
			}
			clients[k] = c
		}
		for k, v := range d.Countries {
			countries[k] += v
		}
		for k, v := range d.Paths {
			paths[k] += v
		}
		for k, v := range d.ErrorPaths {
			errorPaths[k] += v
		}
		for k, v := range d.UnauthorizedPaths {
			unauthorizedPaths[k] += v
		}
		for k, v := range d.RedirectPaths {
			redirectPaths[k] += v
		}
		for k, v := range d.Agents {
			agents[k] += v
		}
		for k, v := range d.Status {
			status[k] += v
		}
		for k, v := range d.Terms {
			terms[k] += v
		}
		for h := range d.Visitors {
			visitors[h] = struct{}{}
		}
	}
	t.Visitors = int64(len(visitors))
	for _, d := range chartSource {
		for k, v := range d.PathBytes {
			pathBytes[k] += v
		}
	}

	// The all-sites index hides the drilldown; skip building the tree for it.
	var drilldown template.HTML
	if r.ShowDrilldown {
		drilldown = pathDrilldown(drilldownPaths(paths, errorPaths, redirectPaths))
	}
	for path, n := range unauthorizedPaths {
		errorPaths[path] -= n
		if errorPaths[path] <= 0 {
			delete(errorPaths, path)
		}
	}
	d := reportData{
		Version:           Version,
		Logo:              template.URL("data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString(assets.Logo)),
		InteractiveCharts: chartHTMLFull(chartSource, r.MonthlyCharts, r.ExternalECharts, omitDayTable),
		PathDrilldown:     drilldown,
		ShowDrilldown:     r.ShowDrilldown,
		Generated:         r.Generated,
		Title:             r.Title,
		IndexHref:         r.IndexHref,
		Years:             r.Years, Months: r.Months, Vhosts: r.Vhosts,
		VhostSummary:       r.VhostSummary,
		Favicon:            template.URL("data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString(assets.Icon)),
		Live:               r.Live,
		InvalidLines:       r.InvalidLines,
		Days:               days,
		T:                  t,
		TopPaths:           topN(paths, 10),
		TopErrorPaths:      topN(errorPaths, 10),
		TopPathsBytes:      topN(pathBytes, 10),
		TopRefs:            topN(refs, 10),
		TopAgents:          topN(agents, 10),
		TopStatus:          statusTop(status),
		TopTerms:           topN(terms, 10),
		TopClientsRequests: topClients(clients, 10, false),
		TopClientsBytes:    topClients(clients, 10, true),
		TopCountries:       topN(countries, 10),
		HasDays:            len(days) > 0,
		HasVhostCharts:     distinctHosts(chartSource) > 1,
		GeoEnabled:         r.GeoEnabled,
	}
	// Failsafe: only the three known palette values may reach the DOM.
	switch r.Palette {
	case "forest", "ember":
		d.Palette = r.Palette
	default:
		d.Palette = "ocean"
	}
	if d.Title == "" {
		d.Title = "Traffic report"
	}
	if d.HasDays {
		// Daily averages use the calendar span from the first to the last
		// data date, so zero-traffic gaps count as days too.
		first, last := days[0].Date, days[len(days)-1].Date
		if a, b, ok := parseDayDates(first, last); ok {
			d.Period = first + " – " + last
			d.AvgDenominator = int64(b.Sub(a).Hours()/24) + 1
			if d.AvgDenominator > 0 {
				n := float64(d.AvgDenominator)
				d.AvgRequests, d.AvgBytes = float64(t.Requests)/n, float64(t.Bytes)/n
				d.AvgFiles, d.AvgPages, d.AvgVisits = float64(t.Files)/n, float64(t.Pages)/n, float64(t.Visits)/n
				d.AvgVisitors = float64(dailyVisitors) / n
			}
		}
	}
	return d
}

func topN(m map[string]int64, n int) []top {
	out := make([]top, 0, len(m))
	for k, v := range m {
		out = append(out, top{k, v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func externalReferrers(days []Day) map[string]int64 {
	byHost := map[string][]Day{}
	for _, d := range days {
		byHost[d.Vhost] = append(byHost[d.Vhost], d)
	}
	refs := map[string]int64{}
	for source, days := range byHost {
		site := inferSiteName(source, days)
		for _, d := range days {
			for ref, count := range d.Referrers {
				if RefererExternal(ref, site) {
					refs[ref] += count
				}
			}
		}
	}
	return refs
}

// classifyOS buckets a raw user agent by the first operating system it names.
// Browsers name the OS in the parentheses before the product list; curl and
// other plain agents carry no OS and fall back to the product name. Crawlers
// often copy a browser user agent and report its OS, so they are bucketed
// separately before the OS is read.
func classifyOS(agent string) string {
	agent = strings.ToLower(strings.TrimSpace(agent))
	if isCrawler(agent) {
		return "Crawler"
	}
	for _, os := range []string{"windows", "ipad", "iphone", "ipod", "macintosh", "mac os", "android", "linux"} {
		if strings.Contains(agent, os) {
			return os
		}
	}
	// Plain agents name their product first, e.g. "curl/8.0".
	if i := strings.IndexAny(agent, " /"); i > 0 {
		return agent[:i]
	}
	if agent == "" {
		return "Unknown"
	}
	return agent
}

// isCrawler reports whether a user agent names itself as a crawler or bot, or
// carries the "(compatible; ...)" trailer such agents use.
func isCrawler(agent string) bool {
	for _, marker := range []string{"bot", "crawler", "spider", "slurp", "compatible;", "headless"} {
		if strings.Contains(agent, marker) {
			return true
		}
	}
	return false
}

// statusClass groups an HTTP status code into its fixed colour range.
func statusClass(code string) string {
	n, err := strconv.Atoi(code)
	if err != nil || n < 200 || n > 599 {
		return "Other"
	}
	return strconv.Itoa(n/100) + "xx"
}

// statusTop labels codes with net/http.StatusText, falling back to
// "Unknown Status".
func statusTop(m map[string]int64) []top {
	out := topN(m, 10)
	for i := range out {
		code, _ := strconv.Atoi(out[i].Name)
		name := http.StatusText(code)
		if name == "" {
			name = "Unknown Status"
		}
		out[i].Name += " " + name
	}
	return out
}

func topClients(m map[string]ClientStats, n int, byBytes bool) []clientEntry {
	out := make([]clientEntry, 0, len(m))
	for id, c := range m {
		name, raw := id, false
		if c.Raw != "" {
			name, raw = c.Raw, true
		}
		out = append(out, clientEntry{Name: name, Requests: c.Requests, Bytes: c.Bytes, Raw: raw})
	}
	sort.Slice(out, func(i, j int) bool {
		if byBytes {
			if out[i].Bytes != out[j].Bytes {
				return out[i].Bytes > out[j].Bytes
			}
		} else if out[i].Requests != out[j].Requests {
			return out[i].Requests > out[j].Requests
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// trailingTwelveMonths keeps the days from the first day of the month 11
// months before the last data month through the end. It anchors on the last
// data point (not today) so old snapshots render a stable window. If less than
// a year of data exists the cutoff precedes the first day and all days are
// returned.
func trailingTwelveMonths(days []Day) []Day {
	if len(days) == 0 {
		return nil
	}
	last, err := time.Parse("2006-01-02", days[len(days)-1].Date)
	if err != nil {
		return nil
	}
	cutoff := time.Date(last.Year(), last.Month(), 1, 0, 0, 0, 0, last.Location()).AddDate(0, -11, 0).Format("2006-01-02")
	out := make([]Day, 0, len(days))
	for _, d := range days {
		if d.Date >= cutoff {
			out = append(out, d)
		}
	}
	return out
}

func parseDayDates(first, last string) (a, b time.Time, ok bool) {
	f, err1 := time.Parse("2006-01-02", first)
	l, err2 := time.Parse("2006-01-02", last)
	if err1 != nil || err2 != nil || f.After(l) {
		return f, l, false
	}
	return f, l, true
}

// group formats a non-negative integer with thousands separators.
func group(v int64) string {
	s := strconv.FormatInt(v, 10)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return strconv.FormatInt(b, 10) + " B"
	}
	return humanAverageBytes(float64(b))
}

func humanAverageBytes(f float64) string {
	const unit = 1024
	if f < unit {
		return strconv.FormatFloat(f, 'f', 1, 64) + " B"
	}
	for _, u := range []string{"KB", "MB", "GB", "TB", "PB", "EB"} {
		f /= unit
		if f < unit {
			return strconv.FormatFloat(f, 'f', 1, 64) + " " + u
		}
	}
	return strconv.FormatFloat(f, 'f', 1, 64) + " EB"
}

func humanByteRate(f float64) string {
	if f < 1024 {
		return strconv.FormatFloat(f, 'f', 3, 64) + " B/s"
	}
	return humanAverageBytes(f) + "/s"
}

// reportHTML is the page template; the file stays editable as plain HTML.
//
//go:embed report.html
var reportHTML string
