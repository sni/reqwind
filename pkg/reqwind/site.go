package reqwind

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Page struct {
	Path   string
	Report Report
}

type Summary struct {
	Name, Href      string
	Requests, Bytes int64
	Visitors        int
}

// BuildPages returns the index first and self-contained detail pages beside
// it: per-year, per-month, per-vhost, and per-vhost year/month pages.
func BuildPages(index string, report Report) []Page {
	dir, base := filepath.Dir(index), filepath.Base(index)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	indexHref := url.PathEscape(base)
	byYear, byMonth, hostDays := map[string][]Day{}, map[string][]Day{}, map[string][]Day{}
	for _, d := range report.Days {
		if len(d.Date) != 10 {
			continue
		}
		byYear[d.Date[:4]] = append(byYear[d.Date[:4]], d)
		byMonth[d.Date[:7]] = append(byMonth[d.Date[:7]], d)
		host := d.Vhost
		if host == "" {
			host = "(unknown)"
		}
		hostDays[host] = append(hostDays[host], d)
	}
	// Per-host yearly/monthly buckets and a stable page identity per host.
	type hostRef struct {
		name   string
		hash   string
		days   []Day
		years  map[string][]Day
		months map[string][]Day
	}
	hosts := map[string]*hostRef{}
	for _, h := range sortedDayKeys(hostDays, false) {
		ref := &hostRef{days: hostDays[h], years: map[string][]Day{}, months: map[string][]Day{}}
		for _, d := range ref.days {
			ref.years[d.Date[:4]] = append(ref.years[d.Date[:4]], d)
			ref.months[d.Date[:7]] = append(ref.months[d.Date[:7]], d)
		}
		// Guess only the display name; keep source groups and their URLs stable.
		ref.name = inferSiteName(h, ref.days)
		if ref.name == "" {
			ref.name = machineHostname()
		}
		digest := sha256.Sum256([]byte(h))
		ref.hash = fmt.Sprintf("%x", digest[:16])
		hosts[h] = ref
	}
	mkReport := func(title, vhostSub string, days []Day) Report {
		r := report
		r.Title, r.IndexHref, r.MonthlyCharts, r.Days = title, indexHref, false, days
		r.Years, r.Months, r.Vhosts = nil, nil, nil
		r.VhostSummary, r.ShowDrilldown = vhostSummaries(stem, days, vhostSub), true
		return r
	}
	var pages []Page
	addPage := func(href string, r Report) { pages = append(pages, Page{Path: filepath.Join(dir, href), Report: r}) }

	// All-sites index: prefix drilldown hidden (it is too large across
	// every host).
	indexDays := trailingTwelveMonths(report.Days)
	indexMonths, indexHosts := map[string][]Day{}, map[string][]Day{}
	for _, d := range indexDays {
		indexMonths[d.Date[:7]] = append(indexMonths[d.Date[:7]], d)
		host := d.Vhost
		if host == "" {
			host = "(unknown)"
		}
		indexHosts[host] = append(indexHosts[host], d)
	}
	indexReport := mkReport("All sites", "", indexDays)
	indexReport.IndexHref = ""
	indexReport.ShowDrilldown = false
	// Only the yearly summary keeps the full history on the index.
	indexReport.ChartDays = indexDays
	for _, year := range sortedDayKeys(byYear, true) {
		href := stem + "-year-" + year + ".html"
		yr := mkReport(year, "year-"+year, byYear[year])
		// A yearly report holds up to 365 per-day rows; drop the table there.
		yr.OmitDayTable = true
		addPage(href, yr)
		indexReport.Years = append(indexReport.Years, sumOf(year, href, byYear[year]))
	}
	for _, month := range sortedDayKeys(byMonth, true) {
		href := stem + "-month-" + month + ".html"
		addPage(href, mkReport(month, "month-"+month, byMonth[month]))
		if days := indexMonths[month]; len(days) > 0 {
			indexReport.Months = append(indexReport.Months, sumOf(month, href, days))
		}
	}
	hostNames := make([]string, 0, len(hosts))
	for h := range hosts {
		hostNames = append(hostNames, h)
	}
	sort.Strings(hostNames)
	for _, h := range hostNames {
		ref := hosts[h]
		vhostHref := fmt.Sprintf("%s-vhost-%s.html", stem, ref.hash)
		vhostReport := mkReport(ref.name, "", ref.days)
		// The vhost page's year/month lists point to its own sub-pages. The
		// page itself names the host; the summary rows only carry the period.
		for _, year := range sortedDayKeys(ref.years, true) {
			href := fmt.Sprintf("%s-vhost-%s-year-%s.html", stem, ref.hash, year)
			addPage(href, mkReport(ref.name+" · "+year, "", ref.years[year]))
			vhostReport.Years = append(vhostReport.Years, sumOf(year, href, ref.years[year]))
		}
		for _, month := range sortedDayKeys(ref.months, true) {
			href := fmt.Sprintf("%s-vhost-%s-month-%s.html", stem, ref.hash, month)
			addPage(href, mkReport(ref.name+" · "+month, "", ref.months[month]))
			vhostReport.Months = append(vhostReport.Months, sumOf(month, href, ref.months[month]))
		}
		addPage(vhostHref, vhostReport)
		indexReport.Vhosts = append(indexReport.Vhosts, sumOf(ref.name, vhostHref, indexHosts[h]))
	}
	// Every page offers the full site select in its header.
	for i := range pages {
		pages[i].Report.Vhosts = indexReport.Vhosts
	}
	return append([]Page{{Path: index, Report: indexReport}}, pages...)
}

// sumOf totals the requests, bytes and unique visitors of a day set into one
// summary row.
func sumOf(name, href string, days []Day) Summary {
	s := Summary{Name: name, Href: url.PathEscape(href)}
	unique := map[string]bool{}
	for _, d := range days {
		s.Requests += d.Requests
		s.Bytes += d.Bytes
		for visitor := range d.Visitors {
			unique[visitor] = true
		}
	}
	s.Visitors = len(unique)
	return s
}

// vhostSummaries builds the per-host summary rows for one page, so year and
// month pages carry their own per-host totals, not the all-time ones. The
// vhostSub suffix ("year-YYYY", "month-YYYY-MM", or "") points each row at
// the matching per-vhost sub-page instead of the full vhost page.
func vhostSummaries(stem string, days []Day, vhostSub string) []Summary {
	byHost := map[string][]Day{}
	for _, d := range days {
		host := d.Vhost
		if host == "" {
			host = "(unknown)"
		}
		byHost[host] = append(byHost[host], d)
	}
	out := make([]Summary, 0, len(byHost))
	for _, host := range sortedDayKeys(byHost, false) {
		hash := sha256.Sum256([]byte(host))
		name := inferSiteName(host, byHost[host])
		if name == "" {
			name = machineHostname()
		}
		href := fmt.Sprintf("%s-vhost-%x%s.html", stem, hash[:16], subSuffix(vhostSub))
		out = append(out, sumOf(name, href, byHost[host]))
	}
	return out
}

// subSuffix renders the per-vhost sub-page suffix, empty for the base vhost
// page. vhostSub values come from report.Days dates, never from input.
func subSuffix(vhostSub string) string {
	if vhostSub == "" {
		return ""
	}
	return "-" + vhostSub
}

// inferSiteName names a fallback group (no explicit -vhost or parsed vhost
// prefix) after its referrer majority: the referrer hostname holding a
// strict majority of the external, well-formed http(s) referrer requests
// (recognized search engines do not count). Explicit hostnames and the
// machine hostname are never rewritten. Ties and empty results yield "".
func inferSiteName(host string, days []Day) string {
	if !strings.HasPrefix(host, "log:") || !strings.Contains(host, " [") || !strings.HasSuffix(host, "]") {
		return host
	}
	counts, total := map[string]int64{}, int64(0)
	for _, d := range days {
		for ref, n := range d.Referrers {
			hostname, ok := referrerHostname(ref)
			if !ok {
				continue
			}
			counts[hostname] += n
			total += n
		}
	}
	for hostname, n := range counts {
		if n*2 > total {
			return hostname
		}
	}
	return ""
}

func referrerHostname(ref string) (string, bool) {
	u, err := url.Parse(ref)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	searchParam, _ := searchTermFor(host, "")
	if host == "" || searchParam != "" {
		return "", false
	}
	return host, true
}

func machineHostname() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "(unknown)"
}

func sortedDayKeys(m map[string][]Day, descending bool) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if descending {
		sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	}
	return keys
}

func groupDays(days []Day, prefix int) []Day {
	groups := map[string]*Day{}
	for _, d := range days {
		date := d.Date
		if len(date) > prefix {
			date = date[:prefix]
		}
		g := groups[date]
		if g == nil {
			g = &Day{Date: date, Status: map[string]int64{}, Paths: map[string]int64{}, Referrers: map[string]int64{}, Agents: map[string]int64{}, Terms: map[string]int64{}, Visitors: map[string]bool{}, Clients: map[string]ClientStats{}, Countries: map[string]int64{}, CountryVisitors: map[string]map[string]bool{}}
			groups[date] = g
		}
		g.Requests += d.Requests
		g.Bytes += d.Bytes
		g.Files += d.Files
		g.Pages += d.Pages
		g.Visits += d.Visits
		for id, c := range d.Clients {
			cc := g.Clients[id]
			cc.Requests += c.Requests
			cc.Bytes += c.Bytes
			if cc.Raw == "" {
				cc.Raw = c.Raw
			}
			g.Clients[id] = cc
		}
		for i, n := range d.Hours {
			g.Hours[i] += n
		}
		for visitor := range d.Visitors {
			g.Visitors[visitor] = true
		}
		for country, ids := range d.CountryVisitors {
			if g.CountryVisitors[country] == nil {
				g.CountryVisitors[country] = map[string]bool{}
			}
			for id := range ids {
				g.CountryVisitors[country][id] = true
			}
		}
		if g.PathBytes == nil {
			g.PathBytes = map[string]int64{}
		}
		if g.ErrorPaths == nil {
			g.ErrorPaths = map[string]int64{}
		}
		if g.UnauthorizedPaths == nil {
			g.UnauthorizedPaths = map[string]int64{}
		}
		if g.RedirectPaths == nil {
			g.RedirectPaths = map[string]int64{}
		}
		for _, pair := range [][2]map[string]int64{{g.Status, d.Status}, {g.Paths, d.Paths}, {g.PathBytes, d.PathBytes}, {g.ErrorPaths, d.ErrorPaths}, {g.UnauthorizedPaths, d.UnauthorizedPaths}, {g.RedirectPaths, d.RedirectPaths}, {g.Referrers, d.Referrers}, {g.Agents, d.Agents}, {g.Terms, d.Terms}, {g.Countries, d.Countries}} {
			for key, n := range pair[1] {
				pair[0][key] += n
			}
		}
	}
	result := make([]Day, 0, len(groups))
	for _, d := range groups {
		result = append(result, *d)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Date < result[j].Date })
	return result
}
