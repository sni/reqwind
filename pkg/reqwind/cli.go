package reqwind

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Run analyzes logs and writes the static report site.
func Run(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("reqwind", flag.ContinueOnError)
	fs.SetOutput(out)
	version := fs.Bool("version", false, "print version and exit")
	var snapshots []string
	fs.Func("merge-state", "render saved state files without reading logs or writing state (repeatable path or glob)", func(path string) error {
		if path == "" {
			return errors.New("merge-state path must not be empty")
		}
		snapshots = append(snapshots, path)
		return nil
	})
	output := fs.String("output", "index.html", "HTML index path (detail pages go beside it)")
	statePath := fs.String("state", ".reqwind", "private incremental state meta file (month buckets sit beside it)")
	var vhosts vhostFlags
	fs.Var(&vhosts, "vhost", "assign all requests in a log: hostname=path-or-glob (repeatable; keeps explicit ports)")
	live := fs.Bool("live", false, "watch logs and refresh the report until interrupted")
	verbose := fs.Bool("verbose", false, "show per-file progress, checkpoints, local GeoIP availability, written files, and the first 10 invalid lines")
	palette := fs.String("palette", "ocean", "default report palette: ocean, forest, or ember")
	geoDB := fs.String("geo-db", "", "local GeoIP database (default auto-detect; off disables lookup)")
	hashClients := fs.Bool("hash-clients", false, "store and show HMAC identifiers instead of raw client IPs")
	noExternalAssets := fs.Bool("inline-assets", false, "embed the chart library and world map into every page instead of the shared files (default)")
	dailyPages := fs.Bool("daily-pages", false, "add per-day summary pages (off by default: a year of data adds 365 pages)")
	interval := fs.Duration("interval", 2*time.Second, "live polling interval")
	fs.Usage = func() {
		fmt.Fprintln(out, "Usage: reqwind [options] [access.log ...]\n       reqwind -merge-state path-or-glob [-merge-state path-or-glob ...] [options]\nLogs may be quoted glob patterns. Defaults to standard Apache/nginx paths.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *version {
		fmt.Fprintln(out, "reqwind "+Version)
		return nil
	}
	if *interval < 100*time.Millisecond {
		return errors.New("interval must be at least 100ms")
	}
	if *palette != "ocean" && *palette != "forest" && *palette != "ember" {
		return errors.New("palette must be ocean, forest, or ember")
	}
	if *output == "" || *statePath == "" {
		return errors.New("output and state paths must not be empty")
	}
	// Reset on every run so a previous run's writer (or a test's) cannot leak.
	wroteFile = nil
	if *verbose {
		wroteFile = out
	}
	if tz := strings.TrimPrefix(os.Getenv("TZ"), ":"); tz != "" {
		if _, err := time.LoadLocation(tz); err != nil {
			return fmt.Errorf("invalid TZ %q; use an IANA timezone such as Europe/Berlin or UTC: %w", tz, err)
		}
	}
	outputAbs, err := filepath.Abs(*output)
	if err != nil {
		return err
	}
	stateAbs, err := filepath.Abs(*statePath)
	if err != nil {
		return err
	}
	if outputAbs == stateAbs || outputAbs == stateAbs+".lock" {
		return errors.New("output and state/lock paths must differ")
	}
	if len(snapshots) > 0 {
		if len(fs.Args()) > 0 || len(vhosts) > 0 || *live {
			return errors.New("merge-state cannot be combined with log inputs, vhost mappings, or live mode")
		}
		paths, err := expandLogs(snapshots)
		if err != nil {
			return err
		}
		return renderStateReports(paths, outputAbs, time.Now(), *palette, *hashClients, !*noExternalAssets, *dailyPages, out)
	}
	patterns := fs.Args()
	for _, mapping := range vhosts {
		_, pattern, _ := strings.Cut(mapping, "=")
		patterns = append(patterns, pattern)
	}
	defaults := len(patterns) == 0
	lock, err := os.OpenFile(stateAbs+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("another reqwind process is using this state")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	s, err := loadState(stateAbs)
	if err != nil {
		return err
	}
	s.ctx = ctx
	if *hashClients {
		s.hashClientAddresses()
	}
	lookup, geoDescription, err := loadGeo(ctx, *geoDB)
	if err != nil {
		return err
	}
	s.geo = lookup
	if *verbose {
		fmt.Fprintf(out, "reqwind: state %q, %d checkpoints; GeoIP: %s\n", stateAbs, len(s.Files), geoDescription)
		invalidShown := 0
		s.invalidLine = func(path string, offset int64, line []byte, err error) {
			fmt.Fprintf(out, "reqwind: %q: invalid line at decoded byte %d (%v): %q\n", path, offset, err, strings.TrimSuffix(string(line), "\n"))
			invalidShown++
			if invalidShown == 10 {
				s.invalidLine = nil
			}
		}
	}
	process := func() error {
		now := time.Now()
		var paths []string
		var err error
		if defaults {
			paths, err = discoverLogs(defaultLogBases)
		} else {
			paths, err = expandLogs(patterns)
		}
		if err != nil {
			return err
		}
		if err := checkTargets(paths, []string{outputAbs, stateAbs, stateAbs + ".lock"}); err != nil {
			return err
		}
		groups, err := vhosts.expand()
		if err != nil {
			return err
		}
		var total scanStats
		// Lines and bytes already read in earlier runs are skipped, not
		// re-parsed; they are the totals of the prior checkpoints.
		var oldLines, oldBytes int64
		for _, cp := range s.Files {
			oldLines += cp.TotalLines
			oldBytes += cp.Offset
		}
		ingestStart := time.Now()
		for _, p := range paths {
			group := groups[p]
			if group == "" {
				group = sourceVhost(p)
			}
			if *verbose {
				fmt.Fprintf(out, "reqwind: reading %q as %q\n", p, group)
			}
			fileStart := time.Now()
			st, err := s.ingest(p, now, groups[p])
			if err != nil {
				return fmt.Errorf("%s: %w", p, err)
			}
			if *verbose {
				fmt.Fprintf(out, "reqwind: %q: %d new lines, %d invalid, %d new decoded bytes in %s\n", p, st.Lines, st.Invalid, st.Bytes, time.Since(fileStart).Round(time.Millisecond))
			}
			total.Lines += st.Lines
			total.Invalid += st.Invalid
			total.Bytes += st.Bytes
		}
		report := s.report(now, *live)
		report.Palette, report.GeoEnabled, report.DailyPages = *palette, lookup != nil, *dailyPages
		if !*noExternalAssets {
			report.ExternalECharts = true
			if err := writeSharedAssets(outputAbs); err != nil {
				return err
			}
		}
		pages := BuildPages(outputAbs, report)
		if err := writeReports(s, stateAbs, outputAbs, pages, paths, out); err != nil {
			return err
		}
		fmt.Fprintf(out, "reqwind: %d new lines, %d old lines, %d invalid, %d new decoded bytes, %d old decoded bytes in %s; %d months, %d pages → %s\n", total.Lines, oldLines, total.Invalid, total.Bytes, oldBytes, time.Since(ingestStart).Round(time.Millisecond), len(s.Months), len(pages), *output)
		return nil
	}
	if err := process(); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	if !*live {
		return nil
	}
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := process(); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
		}
	}
}

type vhostFlags []string

func (v *vhostFlags) String() string { return strings.Join(*v, ", ") }
func (v *vhostFlags) Set(value string) error {
	host, pattern, ok := strings.Cut(value, "=")
	if !ok || strings.TrimSpace(host) == "" || pattern == "" {
		return errors.New("vhost must be hostname=path-or-glob")
	}
	*v = append(*v, value)
	return nil
}

func (v vhostFlags) expand() (map[string]string, error) {
	groups := map[string]string{}
	for _, value := range v {
		host, pattern, _ := strings.Cut(value, "=")
		paths, err := expandLogs([]string{pattern})
		if err != nil {
			return nil, err
		}
		for _, path := range paths {
			if previous, ok := groups[path]; ok && previous != host {
				return nil, fmt.Errorf("conflicting vhost mappings for %s", path)
			}
			groups[path] = host
		}
	}
	return groups, nil
}

func sourceVhost(path string) string {
	path = rotationBase(path)
	if dir := filepath.Base(filepath.Dir(path)); dir == "archive" || dir == "old" {
		path = filepath.Join(filepath.Dir(filepath.Dir(path)), filepath.Base(path))
	}
	h := sha256.Sum256([]byte(path))
	return fmt.Sprintf("log:%s [%x]", filepath.Base(path), h[:8])
}

func rotationBase(path string) string {
	if strings.HasSuffix(strings.ToLower(path), ".gz") {
		path = path[:len(path)-3]
	}
	for _, layout := range []string{"2006-01-02", "20060102"} {
		i := len(path) - len(layout)
		if i > 0 && (path[i-1] == '.' || path[i-1] == '-') {
			if _, err := time.Parse(layout, path[i:]); err == nil {
				return path[:i-1]
			}
		}
	}
	if i := strings.LastIndexByte(path, '.'); i >= 0 {
		if _, err := strconv.ParseUint(path[i+1:], 10, 64); err == nil {
			path = path[:i]
		}
	}
	return path
}

func checkTargets(inputs, targets []string) error {
	byPath, byFile := map[string]bool{}, map[string]bool{}
	fileID := func(info os.FileInfo) string {
		st := info.Sys().(*syscall.Stat_t)
		return fmt.Sprintf("%d:%d", st.Dev, st.Ino)
	}
	for _, path := range targets {
		abs, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		dir, err := filepath.EvalSymlinks(filepath.Dir(abs))
		if err != nil {
			return err
		}
		abs = filepath.Join(dir, filepath.Base(abs))
		if byPath[abs] {
			return fmt.Errorf("output/state paths overlap: %s", path)
		}
		byPath[abs] = true
		if info, err := os.Stat(path); err == nil {
			id := fileID(info)
			if byFile[id] {
				return fmt.Errorf("output/state files alias: %s", path)
			}
			byFile[id] = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	for _, path := range inputs {
		abs, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if byPath[abs] || byFile[fileID(info)] {
			return fmt.Errorf("log input overlaps output/state: %s", path)
		}
	}
	return nil
}

func expandLogs(patterns []string) ([]string, error) {
	seen := map[string]bool{}
	var paths []string
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, err
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("no logs match %q", pattern)
		}
		for _, p := range matches {
			info, err := os.Stat(p)
			if err != nil {
				return nil, err
			}
			// A folder selects the logs and zipped logs inside it.
			if info.IsDir() {
				entries, err := os.ReadDir(p)
				if err != nil {
					return nil, err
				}
				for _, e := range entries {
					if e.Type().IsRegular() && !strings.Contains(e.Name(), "error.log") {
						addLogPath(&seen, &paths, filepath.Join(p, e.Name()))
					}
				}
				continue
			}
			addLogPath(&seen, &paths, p)
		}
	}
	if len(paths) == 0 {
		return nil, errors.New("no logs match the given paths")
	}
	sort.Strings(paths)
	return paths, nil
}

// addLogPath appends a regular file path exactly once.
func addLogPath(seen *map[string]bool, paths *[]string, path string) {
	abs, err := filepath.Abs(path)
	if err != nil {
		*paths = append(*paths, path)
		return
	}
	if !(*seen)[abs] {
		(*seen)[abs] = true
		*paths = append(*paths, abs)
	}
}
