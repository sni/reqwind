package reqwind

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const maxLine = 1024 * 1024
const stateVersion = 1
const gzipMagic = "\x1f\x8b"

// shortcut: keep 10,000 labels per daily table; use a heavy-hitter sketch if exact top ranks beyond that are needed.
const maxKeys = 10000

type checkpoint struct {
	Offset int64
	// TotalLines is the cumulative line count (valid + invalid) read from this
	// file across all runs, so a run can report how many lines were already
	// read ("old") without re-parsing them.
	TotalLines int64
	Head, Tail string
	Source     string
	// Compressed, PhysicalSize, and Modified describe the current file on disk.
	Compressed               bool   `json:",omitempty"`
	PhysicalSize             int64  `json:",omitempty"`
	Modified                 int64  `json:",omitempty"`
	ArchiveHead, ArchiveTail string `json:",omitempty"`
	Path                     string `json:",omitempty"`
}

type Month struct {
	Hosts map[string]map[string]*Day
}

type State struct {
	Version int
	Salt    string
	// All day, hour, month and retention buckets are stored in UTC. The
	// field is empty for new and legacy states. A non-UTC value is an
	// interim snapshot saved while buckets followed the local zone; it is
	// rejected on load, never reinterpreted.
	Timezone string `json:",omitempty"`
	Files    map[string]checkpoint
	// Months is kept out of the meta file; each month lives in its own
	// bucket file (path + "-YYYY-MM.json").
	Months       map[string]*Month `json:"-"`
	InvalidLines int64
	Outputs      map[string]map[string]string  `json:",omitempty"`
	Sessions     map[string]map[string][]Visit `json:",omitempty"`
	geo          GeoLookup
	ctx          context.Context
	hashClients  bool
	invalidLine  func(string, int64, []byte, error)
	dirtyMonths  map[string]bool // nil means all months need a first save.
	fileIndex    map[checkpointKey]map[string]bool
}

type scanStats struct{ Lines, Invalid, Bytes int64 }

// utcZoneName reports whether a stored Timezone value is a UTC alias.
func utcZoneName(name string) bool {
	switch name {
	case "", "UTC", "Etc/UTC", "Etc/GMT", "GMT":
		return true
	}
	return false
}

// State is stored as a small meta file at path plus one bucket file per month
// (path + "-YYYY-MM.json") beside it. The meta file holds the salt, file
// checkpoints, session intervals, and output manifest; month buckets hold
// the daily aggregates. Deleting a month is a manual rm of that one file.
// Files contain gzip JSON; older plain JSON files still load.
func loadState(path string) (*State, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return freshState()
	}
	if err != nil {
		return nil, err
	}
	var s State
	if err := unmarshalState(b, &s); err != nil {
		return nil, fmt.Errorf("invalid state meta %s: %w", path, err)
	}
	if err := validMeta(&s); err != nil {
		return nil, err
	}
	if s.Months == nil {
		s.Months = map[string]*Month{}
	}
	s.dirtyMonths = map[string]bool{}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	stem := filepath.Base(path)
	for _, e := range entries {
		if e.IsDir() || !monthFile(stem, e.Name()) {
			continue
		}
		month := monthKey(stem, e.Name())
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		if info.Mode().Perm() != 0600 {
			s.dirtyMonths[month] = true
		}
		mb, err := os.ReadFile(filepath.Join(filepath.Dir(path), e.Name()))
		if err != nil {
			return nil, err
		}
		if !bytes.HasPrefix(mb, []byte(gzipMagic)) {
			s.dirtyMonths[month] = true
		}
		var m *Month
		if err := unmarshalState(mb, &m); err != nil || m == nil || m.Hosts == nil {
			return nil, fmt.Errorf("invalid month bucket %s: %w", e.Name(), err)
		}
		if err := validateMonth(month, m); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		s.Months[month] = m
	}
	return &s, nil
}

// unmarshalState accepts both gzip JSON and older plain JSON files.
func unmarshalState(data []byte, value any) error {
	if bytes.HasPrefix(data, []byte(gzipMagic)) {
		r, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return err
		}
		data, err = io.ReadAll(r)
		closeErr := r.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return json.Unmarshal(data, value)
}

func marshalState(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	w, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// freshState returns an empty state with a new salt.
func freshState() (*State, error) {
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	return &State{Version: stateVersion, Salt: hex.EncodeToString(salt), Files: map[string]checkpoint{}, Months: map[string]*Month{}, Sessions: map[string]map[string][]Visit{}}, nil
}

// validMeta checks the invariants of a state meta record.
func validMeta(s *State) error {
	if s.Version != stateVersion {
		return fmt.Errorf("unsupported state version %d (expected %d; not overwritten)", s.Version, stateVersion)
	}
	if len(s.Salt) != 64 || s.Files == nil {
		return errors.New("incomplete state (not overwritten)")
	}
	// Interim snapshots saved while buckets followed the local zone mix
	// zones; refuse them instead of reading them as UTC.
	if !utcZoneName(s.Timezone) {
		return fmt.Errorf("state stores %s buckets, only UTC states are supported; replay the original logs into a new -state file", s.Timezone)
	}
	if s.Sessions == nil {
		s.Sessions = map[string]map[string][]Visit{}
	}
	for host, clients := range s.Sessions {
		if host == "" || clients == nil {
			return errors.New("invalid session state (not overwritten)")
		}
		for id, visits := range clients {
			if id == "" {
				return errors.New("invalid session client (not overwritten)")
			}
			for i, visit := range visits {
				if visit.End < visit.Start || (i > 0 && visit.Start <= visits[i-1].End) {
					return errors.New("unordered session state (not overwritten)")
				}
			}
		}
	}
	return nil
}

// monthFile reports whether name is a bucket file for base (base + "-YYYY-MM.json").
func monthFile(base, name string) bool {
	if !strings.HasPrefix(name, base+"-") || !strings.HasSuffix(name, ".json") {
		return false
	}
	if _, err := time.Parse("2006-01", monthKey(base, name)); err != nil {
		return false
	}
	return true
}

func monthKey(base, name string) string { return name[len(base)+1 : len(name)-len(".json")] }

// validateMonth checks every day in a month bucket.
func validateMonth(month string, m *Month) error {
	for host, days := range m.Hosts {
		if host == "" || days == nil {
			return errors.New("invalid vhost state (not overwritten)")
		}
		for date, d := range days {
			if _, err := time.Parse("2006-01-02", date); err != nil || !strings.HasPrefix(date, month+"-") || d == nil || d.Date != date || d.Vhost != host || d.Status == nil || d.Paths == nil || d.Referrers == nil || d.Agents == nil || d.Visitors == nil || d.Terms == nil || d.Clients == nil || d.Countries == nil {
				return errors.New("incomplete daily state (not overwritten)")
			}
		}
	}
	return nil
}

// wroteFile receives "wrote <path>" lines under -verbose; nil otherwise.
var wroteFile io.Writer

func writeIfChanged(path string, data []byte, mode os.FileMode) error {
	stored, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil && bytes.Equal(stored, data) {
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if info.Mode().Perm() == mode.Perm() {
			return nil
		}
	}
	return atomicWrite(path, mode, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

func atomicWrite(path string, mode os.FileMode, write func(io.Writer) error) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".reqwind-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err == nil {
		err = write(f)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	if wroteFile != nil {
		fmt.Fprintf(wroteFile, "reqwind: wrote %s\n", path)
	}
	return nil
}

// saveState writes one bucket file per month and the meta file. Each file is
// written atomically; the set of files is not one atomic update.
// Month data is never deleted here: retention is manual, so removing a month
// is a rm of that single bucket file.
func (s *State) saveState(path string) error {
	dir, stem := filepath.Dir(path), filepath.Base(path)
	for month, m := range s.Months {
		if s.dirtyMonths != nil && !s.dirtyMonths[month] {
			continue
		}
		b, err := marshalState(m)
		if err != nil {
			return err
		}
		if err := writeIfChanged(filepath.Join(dir, fmt.Sprintf("%s-%s.json", stem, month)), b, 0600); err != nil {
			return err
		}
	}
	b, err := marshalState(s)
	if err != nil {
		return err
	}
	if err := writeIfChanged(path, b, 0600); err != nil {
		return err
	}
	s.dirtyMonths = map[string]bool{}
	return nil
}

func bump(m map[string]int64, key string) {
	if _, ok := m[key]; !ok && len(m) >= maxKeys {
		key = "(other)"
	}
	m[key]++
}

func (s *State) ensureDay(date, host string) *Day {
	if s.dirtyMonths != nil {
		s.dirtyMonths[date[:7]] = true
	}
	m := s.Months[date[:7]]
	if m == nil {
		m = &Month{Hosts: map[string]map[string]*Day{}}
		s.Months[date[:7]] = m
	}
	if m.Hosts[host] == nil {
		m.Hosts[host] = map[string]*Day{}
	}
	d := m.Hosts[host][date]
	if d == nil {
		d = &Day{Date: date, Vhost: host, Status: map[string]int64{}, Paths: map[string]int64{}, Referrers: map[string]int64{}, Agents: map[string]int64{}, Visitors: map[string]bool{}, Terms: map[string]int64{}, Clients: map[string]ClientStats{}, Countries: map[string]int64{}, CountryVisitors: map[string]map[string]bool{}}
		m.Hosts[host][date] = d
	}
	if d.CountryVisitors == nil {
		d.CountryVisitors = map[string]map[string]bool{}
	}
	return d
}

// add stores one entry in its UTC day; the report renders hours in the
// display zone without touching the stored buckets. Future-dated entries are
// skipped; past entries are always kept (retention is manual).
func (s *State) add(e Entry, today string, visitorMAC hash.Hash) error {
	t := e.Time.UTC()
	date := t.Format("2006-01-02")
	if date > today {
		return nil
	}
	d := s.ensureDay(date, e.Vhost)
	visitorMAC.Reset()
	visitorMAC.Write([]byte(e.Host))
	var digest [sha256.Size]byte
	id := hex.EncodeToString(visitorMAC.Sum(digest[:0])[:16])
	d.Visitors[id] = true
	if s.geo != nil {
		country, err := s.geo(e.Host)
		if err != nil {
			return err
		}
		if country == "" {
			country = unknownCountry // geo on but the lookup could not resolve a country
		}
		bump(d.Countries, country)
		if d.CountryVisitors[country] == nil {
			d.CountryVisitors[country] = map[string]bool{}
		}
		d.CountryVisitors[country][id] = true
	}
	d.Requests++
	d.Bytes += e.Bytes
	d.Hours[t.Hour()]++
	bump(d.Status, strconv.Itoa(e.Status))
	bump(d.Paths, e.Path)
	if d.PathBytes == nil {
		d.PathBytes = map[string]int64{}
	}
	path := e.Path
	if _, ok := d.Paths[path]; !ok {
		path = otherLabel
	}
	d.PathBytes[path] += e.Bytes
	if e.Status >= 400 && e.Status < 600 {
		if d.ErrorPaths == nil {
			d.ErrorPaths = map[string]int64{}
		}
		d.ErrorPaths[path]++
	}
	if e.Status == 401 {
		if d.UnauthorizedPaths == nil {
			d.UnauthorizedPaths = map[string]int64{}
		}
		d.UnauthorizedPaths[path]++
	}
	if e.Status >= 300 && e.Status < 400 {
		if d.RedirectPaths == nil {
			d.RedirectPaths = map[string]int64{}
		}
		d.RedirectPaths[path]++
	}
	if term := SearchTerm(e.Referer); term != "" {
		bump(d.Terms, term)
	}
	// Only count external referrers (a different site than the vhost).
	// Query strings can contain credentials; never retain them either.
	if RefererExternal(e.Referer, e.Vhost) {
		ref, _, _ := strings.Cut(e.Referer, "?")
		bump(d.Referrers, ref)
	}
	bump(d.Agents, e.Agent)
	s.trackMetrics(e, d, id)
	return nil
}

func fingerprint(f *os.File, offset int64) (string, string, error) {
	// shortcut: identical prefix/tail rewrites are undetectable; use rename rotation when lossless checkpoints matter.
	n := min(offset, 256)
	a, b := make([]byte, n), make([]byte, n)
	if _, err := f.ReadAt(a, 0); err != nil && n > 0 {
		return "", "", err
	}
	if _, err := f.ReadAt(b, offset-n); err != nil && n > 0 {
		return "", "", err
	}
	ha, hb := sha256.Sum256(a), sha256.Sum256(b)
	return hex.EncodeToString(ha[:]), hex.EncodeToString(hb[:]), nil
}

func (s *State) days() []Day {
	var days []Day
	for _, m := range s.Months {
		for _, dates := range m.Hosts {
			for _, d := range dates {
				days = append(days, *d)
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

func (s *State) report(now time.Time, live bool) Report {
	r := Report{Generated: now, InvalidLines: s.InvalidLines, Live: live, Days: s.days()}
	return r
}
