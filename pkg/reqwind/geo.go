package reqwind

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// GeoLookup returns an ISO country code, Local/private, or an empty unknown result.
type GeoLookup func(addr string) (string, error)

const localPrivate = "Local/private"

var (
	geoCacheCap = 10000
	geoTimeout  = 2 * time.Second
	// Common local installs; GeoLite2-City.mmdb also carries country iso codes.
	geoCandidates = []string{
		"/usr/share/GeoIP/GeoLite2-Country.mmdb",
		"/var/lib/GeoIP/GeoLite2-Country.mmdb",
		"/usr/local/share/GeoIP/GeoLite2-Country.mmdb",
		"/usr/share/GeoIP/GeoLite2-City.mmdb",
		"/var/lib/GeoIP/GeoLite2-City.mmdb",
		"/usr/local/share/GeoIP/GeoLite2-City.mmdb",
		"/usr/share/GeoIP/GeoIP.dat",
		"/var/lib/GeoIP/GeoIP.dat",
		"/usr/local/share/GeoIP/GeoIP.dat",
		"/usr/share/GeoIP/GeoIPv6.dat",
		"/var/lib/GeoIP/GeoIPv6.dat",
	}
)

type geoResolver struct {
	ctx              context.Context
	bin              string
	legacy, ipv6Only bool
	args             func(ip string) []string
	validate         func(out string) string
	mu               sync.Mutex
	cache            map[string]string
}

func loadGeo(ctx context.Context, database string) (GeoLookup, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if database == "off" {
		return nil, "disabled", nil
	}
	if database == "" {
		var unavailable []string
		for _, p := range geoCandidates {
			if r, err := newGeoResolver(p); err == nil {
				r.ctx = ctx
				return r.lookup, p, nil
			} else if !errors.Is(err, os.ErrNotExist) {
				unavailable = append(unavailable, err.Error())
			}
		}
		if len(unavailable) > 0 {
			return nil, strings.Join(unavailable, "; ") + "; country lookup disabled", nil
		}
		return nil, "no GeoIP database found in standard locations; country lookup disabled", nil
	}
	r, err := newGeoResolver(database)
	if err != nil {
		return nil, "", err
	}
	r.ctx = ctx
	return r.lookup, database, nil
}

func newGeoResolver(db string) (*geoResolver, error) {
	fi, err := os.Stat(db)
	if err != nil {
		return nil, fmt.Errorf("geo database %s missing or inaccessible: %w", db, err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("geo database %s missing or not a regular file", db)
	}
	f, err := os.Open(db)
	if err != nil {
		return nil, fmt.Errorf("geo database unreadable: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	ext := strings.ToLower(filepath.Ext(db))
	bin := "geoiplookup"
	var args func(ip string) []string
	var validate func(out string) string
	if ext == ".mmdb" {
		bin = "mmdblookup"
		args = func(ip string) []string { return []string{"--file", db, "--ip", ip, "country", "iso_code"} }
		validate = func(out string) string { return countryCode(out, false) }
	} else {
		if strings.EqualFold(filepath.Base(db), "GeoIPv6.dat") {
			bin = "geoiplookup6"
		}
		args = func(ip string) []string { return []string{"-f", db, ip} }
		validate = func(out string) string { return countryCode(out, true) }
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return nil, fmt.Errorf("geo database %s unusable: %s not found", db, bin)
	}
	return &geoResolver{ctx: context.Background(), bin: path, args: args, validate: validate, cache: map[string]string{}, legacy: ext != ".mmdb", ipv6Only: bin == "geoiplookup6"}, nil
}

// countryCode accepts exactly two ASCII letters (comma-separated fields for .dat).
func countryCode(out string, commaOK bool) string {
	out = strings.TrimSpace(out)
	if commaOK {
		if i := strings.LastIndexByte(out, ':'); i >= 0 {
			out = out[i+1:]
		}
		out, _, _ = strings.Cut(out, ",")
	} else if strings.HasPrefix(out, "\"") {
		value, rest, ok := strings.Cut(out[1:], "\"")
		if !ok || strings.TrimSpace(rest) != "<utf8_string>" {
			return ""
		}
		out = value
	}
	out = strings.ToUpper(strings.TrimSpace(out))
	if len(out) != 2 {
		return ""
	}
	for i := 0; i < 2; i++ {
		if out[i] < 'A' || out[i] > 'Z' {
			return ""
		}
	}
	return out
}

func (g *geoResolver) lookup(addr string) (string, error) {
	if err := g.ctx.Err(); err != nil {
		return "", err
	}
	ip, err := netip.ParseAddr(addr)
	if err != nil {
		return "", nil
	}
	ip = ip.Unmap()
	if ip.IsUnspecified() || ip.IsLoopback() || ip.IsMulticast() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return localPrivate, nil
	}
	if g.legacy && ip.Is6() != g.ipv6Only {
		return "", nil
	}
	key := ip.String()
	g.mu.Lock()
	if v, ok := g.cache[key]; ok {
		g.mu.Unlock()
		return v, nil
	}
	// shortcut: once geoCacheCap entries are cached, new addresses return "" instead of looking up or evicting; fine for access-log geo, add LRU if the working set grows.
	if len(g.cache) >= geoCacheCap {
		g.mu.Unlock()
		return "", nil
	}
	g.mu.Unlock()

	ctx, cancel := context.WithTimeout(g.ctx, geoTimeout)
	defer cancel()
	var stderr strings.Builder
	cmd := exec.CommandContext(ctx, g.bin, g.args(key)...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if err := g.ctx.Err(); err != nil {
			return "", err
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("geo lookup timed out: %w", ctx.Err())
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			message := strings.ToLower(stderr.String() + string(out))
			missing := strings.Contains(message, "could not find an entry for this ip") ||
				strings.Contains(message, "no data was found at the lookup path") ||
				strings.Contains(message, "ip address not found")
			if missing || (ee.ExitCode() == 1 && strings.TrimSpace(message) == "") {
				g.store(key, "")
				return "", nil
			}
			if msg := strings.TrimSpace(stderr.String()); msg != "" {
				return "", fmt.Errorf("%s: %s", filepath.Base(g.bin), msg)
			}
		}
		return "", fmt.Errorf("geo lookup failed: %w", err)
	}
	code := g.validate(string(out))
	g.store(key, code)
	return code, nil
}

func (g *geoResolver) store(key, v string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.cache) < geoCacheCap {
		g.cache[key] = v
	}
}
