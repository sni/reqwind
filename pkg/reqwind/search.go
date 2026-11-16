package reqwind

import (
	"net"
	"net/netip"
	"net/url"
	"strings"
	"unicode/utf8"
)

// maxSearchTerm bounds the retained search term length (bytes).
const maxSearchTerm = 512

// SearchTerm extracts the search query term from a search-engine referrer
// URL. Only well-formed http/https referrers whose host exactly matches a
// known search engine and whose path is that engine's search page are
// accepted; anything else (unknown engines, non-search paths, malformed or
// non-http referrers, missing terms) yields "". The returned term is URL-
// decoded, trimmed, and bounded to 512 bytes.
func SearchTerm(referrer string) string {
	if referrer == "" || referrer == "-" {
		return ""
	}
	u, err := url.Parse(referrer)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	param, ok := searchTermFor(host, u.Path)
	if !ok {
		return ""
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return ""
	}
	term := strings.TrimSpace(query.Get(param))
	if len(term) > maxSearchTerm {
		term = term[:maxSearchTerm]
	}
	for len(term) > 0 && !utf8.ValidString(term) {
		term = term[:len(term)-1]
	}
	return term
}

// searchTermFor maps an exact engine host to the search-page path (without
// trailing slash) and the query parameter carrying the term.
func searchTermFor(host, path string) (string, bool) {
	path = strings.TrimSuffix(path, "/")
	switch {
	case isGoogle(host):
		return "q", path == "/search"
	case host == "bing.com" || host == "www.bing.com":
		return "q", path == "" || path == "/search"
	case host == "duckduckgo.com" || host == "www.duckduckgo.com":
		return "q", path == "" || path == "/html"
	case host == "lite.duckduckgo.com":
		return "q", path == "/lite"
	case host == "yahoo.com" || host == "www.yahoo.com" || host == "search.yahoo.com":
		return "p", path == "/search" || path == "/p"
	case host == "baidu.com" || host == "www.baidu.com":
		return "wd", path == "/s"
	case host == "yandex.com" || host == "yandex.ru" ||
		host == "www.yandex.com" || host == "www.yandex.ru":
		return "text", path == "/search"
	case host == "brave.com" || host == "www.brave.com" || host == "search.brave.com":
		return "q", path == "/search"
	case host == "ecosia.org" || host == "www.ecosia.org":
		return "q", path == "/search"
	case host == "startpage.com" || host == "www.startpage.com":
		return "query", path == "/sp/search"
	}
	return "", false
}

// shortcut: recognize common Google domains; extend this allowlist for other regional domains.
var googleTLDs = map[string]bool{
	"ar": true, "at": true, "au": true, "be": true, "ca": true, "ch": true,
	"com": true, "com.au": true, "com.br": true, "com.mx": true, "co.in": true,
	"com.ar": true, "com.tr": true, "co.nz": true, "co.kr": true, "com.sg": true,
	"co.jp": true, "co.uk": true, "co.za": true, "cz": true, "de": true,
	"dk": true, "es": true, "fi": true, "fr": true, "gr": true, "hu": true,
	"in": true, "it": true, "jp": true, "kr": true, "nl": true, "no": true,
	"nz": true, "pl": true, "pt": true, "ru": true, "se": true, "tr": true,
	"uk": true,
}

// RefererExternal reports whether referrer points to a different site than
// vhost (i.e. external traffic). Empty, "-", and unparseable referrers are
// not external. A subdomain of the vhost counts as internal.
func RefererExternal(referrer, vhost string) bool {
	if referrer == "" || referrer == "-" {
		return false
	}
	u, err := url.Parse(referrer)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return false
	}
	host := normalizedHost(u.Hostname())
	vh := vhost
	if host, _, err := net.SplitHostPort(vhost); err == nil {
		vh = host
	}
	vh = normalizedHost(strings.Trim(vh, "[]"))
	if vh == "" {
		return true
	}
	return !(host == vh || strings.HasSuffix(host, "."+vh))
}

func normalizedHost(host string) string {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Unmap().String()
	}
	return host
}

func isGoogle(host string) bool {
	host = strings.TrimPrefix(host, "www.")
	tld, ok := strings.CutPrefix(host, "google.")
	return ok && googleTLDs[tld]
}
