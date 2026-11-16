// Package reqwind contains a dependency-free parser for Apache/nginx
// common and combined access log lines.
package reqwind

import (
	"errors"
	"net"
	"strconv"
	"strings"
	"time"
)

// Entry is one parsed log line. Vhost is the optional virtual-host prefix
// that precedes the client host ("" when absent).
type Entry struct {
	Time     time.Time
	Vhost    string
	Host     string
	Method   string
	Path     string
	Protocol string
	Status   int
	Bytes    int64
	Referer  string
	Agent    string
}

// timeLayout matches Apache/nginx access log timestamps: 02/Jan/2006:15:04:05 -0700.
const timeLayout = "02/Jan/2006:15:04:05 -0700"

var errMalformed = errors.New("reqwind: malformed log line")

// ParseLine parses one common or combined log line:
//
//	[host] - user [time] "request" status bytes ["referer" "agent"]
//
// An optional vhost token may precede the client host, either bracketed
// ([example.org]) or bare (example.org:443); surrounding brackets are
// stripped, ports are ignored. Bytes of "-" are zero. A request of "-" yields
// empty method/path/protocol without error. Query strings are stripped from
// Path.
func ParseLine(line string) (Entry, error) {
	var e Entry
	line = strings.TrimSuffix(line, "\r")

	// Find the timestamp: a [field] that parses as a log time. An optional
	// vhost token (bracketed or not) may precede the client host.
	var (
		idx  = -1
		rest string
	)
	for {
		i := strings.IndexByte(line[idx+1:], '[')
		if i < 0 {
			break
		}
		i += idx + 1
		// A fixed-size candidate avoids quadratic scans on malformed bracket runs.
		end := i + 1 + len(timeLayout)
		if end < len(line) && line[end] == ']' {
			if t, err := time.Parse(timeLayout, line[i+1:end]); err == nil {
				e.Time = t
				idx = i
				rest = line[end+1:]
				break
			}
		}
		idx = i
	}
	if idx < 0 || rest == "" {
		return e, errMalformed
	}

	// Before '[': host - user, or vhost host - user (3 or 4 fields).
	fields := splitFields(line[:idx])
	var host string
	switch len(fields) {
	case 3:
		host = fields[0]
	case 4:
		// Ports in log prefixes do not create separate sites.
		e.Vhost = fields[0]
		if strings.HasPrefix(e.Vhost, "[") && strings.HasSuffix(e.Vhost, "]") {
			e.Vhost = e.Vhost[1 : len(e.Vhost)-1]
		}
		if name, _, err := net.SplitHostPort(e.Vhost); err == nil {
			e.Vhost = name
		}
		host = fields[1]
	default:
		return e, errMalformed
	}
	if host == "" || host == "-" {
		return e, errMalformed
	}
	e.Host = host

	rest = skipSpaces(rest)
	if rest == "" || rest[0] != '"' {
		return e, errMalformed
	}
	req, rest, err := unquote(rest)
	if err != nil {
		return e, err
	}
	if err := setRequest(&e, req); err != nil {
		return e, err
	}

	rest = skipSpaces(rest)
	st, rest, ok := parseUint(rest)
	if !ok || st < 100 || st > 599 {
		return e, errMalformed
	}
	e.Status = int(st)

	rest = skipSpaces(rest)
	if rest == "" {
		return e, errMalformed
	}
	if rest[0] == '-' && (len(rest) == 1 || rest[1] == ' ') {
		rest = rest[1:]
	} else if b, r, ok := parseUint(rest); ok {
		e.Bytes = b
		rest = r
	} else {
		return e, errMalformed
	}

	rest = skipSpaces(rest)
	if strings.HasPrefix(rest, `"`) {
		e.Referer, rest, err = unquote(rest)
		if err != nil {
			return e, err
		}
		rest = skipSpaces(rest)
		if strings.HasPrefix(rest, `"`) {
			e.Agent, rest, err = unquote(rest)
			if err != nil {
				return e, err
			}
		}
	}
	if skipSpaces(rest) != "" {
		return e, errMalformed
	}
	return e, nil
}

// setRequest splits "METHOD PATH PROTOCOL" and strips the query string.
func setRequest(e *Entry, req string) error {
	if req == "-" {
		return nil
	}
	sp1 := strings.IndexByte(req, ' ')
	sp2 := strings.LastIndexByte(req, ' ')
	if sp1 < 0 || sp2 <= sp1 {
		return errMalformed
	}
	e.Method = req[:sp1]
	e.Path = req[sp1+1 : sp2]
	e.Protocol = req[sp2+1:]
	if e.Method == "" || e.Protocol == "" {
		return errMalformed
	}
	if i := strings.IndexByte(e.Path, '?'); i >= 0 {
		e.Path = e.Path[:i]
	}
	if i := strings.IndexByte(e.Path, '#'); i >= 0 {
		e.Path = e.Path[:i]
	}
	return nil
}

// unquote reads a double-quoted string starting at s[0] and decodes the
// nginx escapes \" and \\. Other backslash sequences pass through verbatim.
// It returns the value, the string after the closing quote, or an error.
func unquote(s string) (string, string, error) {
	if s == "" || s[0] != '"' {
		return "", s, errMalformed
	}
	var b strings.Builder
	s = s[1:]
	for len(s) > 0 {
		j := strings.IndexAny(s, "\"\\")
		if j < 0 {
			return "", s, errMalformed
		}
		b.WriteString(s[:j])
		if s[j] == '"' {
			return b.String(), s[j+1:], nil
		}
		if j+1 < len(s) && (s[j+1] == '"' || s[j+1] == '\\') {
			b.WriteByte(s[j+1])
			s = s[j+2:]
		} else {
			b.WriteByte('\\')
			s = s[j+1:]
		}
	}
	return "", s, errMalformed
}

// parseUint reads a leading non-negative integer.
func parseUint(s string) (int64, string, bool) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, s, false
	}
	if i < len(s) && s[i] != ' ' {
		return 0, s, false
	}
	v, err := strconv.ParseInt(s[:i], 10, 64)
	if err != nil {
		return 0, s, false
	}
	return v, s[i:], true
}

func skipSpaces(s string) string {
	i := 0
	for i < len(s) && s[i] == ' ' {
		i++
	}
	return s[i:]
}

// splitFields returns the non-empty space-separated fields of s.
func splitFields(s string) []string {
	var out []string
	for {
		s = skipSpaces(s)
		if s == "" {
			break
		}
		i := strings.IndexByte(s, ' ')
		if i < 0 {
			out = append(out, s)
			break
		}
		out = append(out, s[:i])
		if len(out) > 4 {
			return nil
		}
		s = s[i+1:]
	}
	return out
}
