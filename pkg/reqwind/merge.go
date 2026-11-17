package reqwind

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// renderStateReports combines existing state files from several machines
// into one report site. It only reads state; it ingests no logs and writes
// no state file.
//
// Client and visitor identifiers are namespaced with a digest of each
// state's salt, so identifiers from different states never dedup. Two states
// that share one salt do union visitor sets; overlapping requests between
// them are still summed. shortcut: to dedup overlapping snapshots, rebuild
// one state from the original logs.
//
// All stored buckets are UTC, so snapshots merge regardless of the display
// timezone (TZ). Only UTC states are accepted; loadState rejects the rest.
//
// shortcut: expired snapshot pages are never deleted; remove old snapshot
// output directories by hand.
func renderStateReports(paths []string, output string, now time.Time, palette string, hashClients, externalECharts, dailyPages bool, out io.Writer) error {
	states := make([]*State, 0, len(paths))
	inputs, reserved := []string{}, []string{}
	seen := map[string]bool{}
	for _, path := range paths {
		abs, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		info, err := os.Stat(abs)
		// loadState creates a fresh state for missing paths; require existing files.
		if err != nil {
			return fmt.Errorf("%s: %w", abs, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", abs)
		}
		// A state is a meta file plus month buckets beside it; skip the
		// buckets so a *.json glob does not treat each month as a state.
		if isMonthBucket(abs) {
			continue
		}
		st := info.Sys().(*syscall.Stat_t)
		id := fmt.Sprintf("%d:%d", st.Dev, st.Ino)
		if seen[id] {
			return fmt.Errorf("duplicate input state (same file): %s", abs)
		}
		seen[id] = true
		s, err := loadState(abs)
		if err != nil {
			return fmt.Errorf("%s: %w", abs, err)
		}
		if hashClients {
			s.hashClientAddresses()
		}
		states = append(states, s)
		// A source state is never an output; snapshot mode creates no
		// incremental state.
		inputs = append(inputs, abs)
		// The source's lock file is a reserved target even when absent, so
		// an output path colliding with it fails before any write.
		reserved = append(reserved, abs+".lock")
	}
	if len(states) == 0 {
		return errors.New("no snapshot state files")
	}
	report := Report{
		Generated: now,
		Days:      combineDays(states),
		Palette:   palette,
		// No lookup runs in snapshot mode; country data is only what the
		// source states stored.
		GeoEnabled:      true,
		ExternalECharts: externalECharts,
		DailyPages:      dailyPages,
	}
	for _, s := range states {
		report.InvalidLines += s.InvalidLines
	}
	outputAbs, err := filepath.Abs(output)
	if err != nil {
		return err
	}
	if externalECharts {
		if err := writeSharedAssets(outputAbs); err != nil {
			return err
		}
	}
	pages := BuildPages(outputAbs, report)
	targets := make([]string, 0, len(pages)+len(reserved))
	for _, p := range pages {
		targets = append(targets, p.Path)
	}
	targets = append(targets, reserved...)
	if externalECharts {
		targets = append(targets,
			filepath.Join(filepath.Dir(outputAbs), echartsAssetName),
			filepath.Join(filepath.Dir(outputAbs), mapAssetName))
	}
	if err := checkTargets(inputs, targets); err != nil {
		return err
	}
	// Details first, index last.
	for i := len(pages) - 1; i >= 0; i-- {
		page := pages[i]
		if err := atomicWrite(page.Path, 0644, func(w io.Writer) error { return RenderReport(w, page.Report) }); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "reqwind: snapshot report from %d state file(s), %d pages → %s\n", len(states), len(pages), output)
	return nil
}

// isMonthBucket reports whether path is a month bucket file (base + "-YYYY-MM.json")
// rather than a state meta file, so a *.json merge glob does not misread buckets.
func isMonthBucket(path string) bool {
	name := filepath.Base(path)
	if !strings.HasSuffix(name, ".json") {
		return false
	}
	base := strings.TrimSuffix(name, ".json")
	// A bucket is <base>-YYYY-MM.json; the month is the final seven chars
	// after the last dash.
	if len(base) < 8 || base[len(base)-8] != '-' {
		return false
	}
	_, err := time.Parse("2006-01", base[len(base)-7:])
	return err == nil
}

// combineDays merges all days of independent state files. Same date/vhost
// pairs from different states stay separate; groupDays sums them when the
// report renders. Source maps are copied for the namespaced identifiers and
// shared (read-only) elsewhere.
func combineDays(states []*State) []Day {
	var days []Day
	for _, s := range states {
		sum := sha256.Sum256([]byte(s.Salt))
		ns := hex.EncodeToString(sum[:16])
		for _, m := range s.Months {
			for _, ds := range m.Hosts {
				for _, d := range ds {
					g := Day{Date: d.Date, Vhost: d.Vhost, Requests: d.Requests, Bytes: d.Bytes, Files: d.Files, Pages: d.Pages, Visits: d.Visits, Hours: d.Hours,
						Status: d.Status, Paths: d.Paths, PathBytes: d.PathBytes, ErrorPaths: d.ErrorPaths, UnauthorizedPaths: d.UnauthorizedPaths, RedirectPaths: d.RedirectPaths, Referrers: d.Referrers, Agents: d.Agents, Terms: d.Terms, Countries: d.Countries}
					g.Visitors = namespaceSet(ns, d.Visitors)
					g.Clients = make(map[string]ClientStats, len(d.Clients))
					for id, c := range d.Clients {
						g.Clients[ns+"-"+id] = c
					}
					g.CountryVisitors = make(map[string]map[string]bool, len(d.CountryVisitors))
					for country, ids := range d.CountryVisitors {
						g.CountryVisitors[country] = namespaceSet(ns, ids)
					}
					days = append(days, g)
				}
			}
		}
	}
	sort.Slice(days, func(i, j int) bool {
		if days[i].Date != days[j].Date {
			return days[i].Date < days[j].Date
		}
		return days[i].Vhost < days[j].Vhost
	})
	return days
}

func namespaceSet(ns string, in map[string]bool) map[string]bool {
	out := make(map[string]bool, len(in))
	for id := range in {
		out[ns+"-"+id] = true
	}
	return out
}
