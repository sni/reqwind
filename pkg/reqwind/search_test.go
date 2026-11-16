package reqwind

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSearchTerm(t *testing.T) {
	cases := []struct {
		ref  string
		want string
	}{
		// Recognized engines and parameters.
		{`https://www.google.com/search?q=hello+world`, "hello world"},
		{`https://google.co.uk/search?q=a+b`, "a b"},
		{`https://google.de/search?q=%E4%B8%AD%E6%96%87`, "中文"},
		{`https://www.bing.com/?q=bing+term`, "bing term"},
		{`https://www.bing.com/search?q=bing+search`, "bing search"},
		{`https://www.duckduckgo.com/?q=duck`, "duck"},
		{`https://duckduckgo.com/?q=ddg+term`, "ddg term"},
		{`https://lite.duckduckgo.com/lite/?q=lite`, "lite"},
		{`https://www.yahoo.com/search?p=yahoo+term`, "yahoo term"},
		{`https://search.yahoo.com/p?p=via+p`, "via p"},
		{`https://www.baidu.com/s?wd=baidu+term`, "baidu term"},
		{`https://www.yandex.ru/search/?text=yandex+term`, "yandex term"},
		{`https://www.brave.com/search?q=brave+term`, "brave term"},
		{`https://www.ecosia.org/search?q=ecosia+term`, "ecosia term"},
		{`https://www.startpage.com/sp/search?query=startpage+term`, "startpage term"},
		// Escaped and whitespace values are decoded and trimmed.
		{`https://www.google.com/search?q=%20a%20b%20`, "a b"},
		{`https://www.bing.com/?q=%25E2%2580%2599`, "%E2%80%99"},
		// Non-search query tokens are ignored.
		{`https://www.google.com/search?hl=en&gl=us&q=`, ""},
		{`https://www.google.com/search?q=%20`, ""},
		{`https://www.bing.com/?src=ie`, ""},
		// Missing or malformed referrers.
		{`https://www.google.com/`, ""},
		{`https://www.google.com/search`, ""},
		{`https://duckduckgo.com/about`, ""},
		{`http://example.com/search?q=nope`, ""},
		{`ftp://www.google.com/search?q=ftp`, ""},
		{`javascript:alert(1)`, ""},
		{``, ""},
		{`-`, ""},
		{`not a url`, ""},
		{`https://www.google.com/search?q=?%zzzz`, ""},
		{`https://www.google.com/search?q=valid&other=%zz`, ""},
		// Spoofed domains must not match (exact host match, whitelist).
		{`https://google.evil.com/search?q=spoofed`, ""},
		{`https://www.google.com.evil.com/search?q=spoofed`, ""},
		{`https://evilgoogle.com/search?q=spoofed`, ""},
		{`https://search.evil.com/sp/search?query=spoofed`, ""},
		{`https://www2.google.com/search?q=spoofed`, ""},
		{`https://evil.com/@google.com/search?q=spoofed`, ""},
		// Case-insensitive host, port allowed.
		{`https://WWW.GOOGLE.COM/search?q=case`, "case"},
		{`https://www.google.com:443/search?q=port`, "port"},
		// Term longer than 512 bytes is bounded.
		{`https://www.google.com/search?q=` + strings.Repeat(`a`, 600), strings.Repeat(`a`, 512)},
	}
	for _, c := range cases {
		if got := SearchTerm(c.ref); got != c.want {
			t.Errorf("SearchTerm(%q) = %q, want %q", c.ref, got, c.want)
		}
	}
}

// TestSearchTermBoundedBytes checks the bound counts bytes, not runes.
func TestSearchTermBoundedBytes(t *testing.T) {
	long := "a" + strings.Repeat("é", 300) // 1 + 600 bytes, 301 runes
	got := SearchTerm(`https://www.google.com/search?q=` + long)
	if len(got) > maxSearchTerm || !utf8.ValidString(got) || got != long[:511] {
		t.Errorf("invalid bounded term: bytes=%d valid=%t", len(got), utf8.ValidString(got))
	}
}

func TestRefererExternal(t *testing.T) {
	cases := []struct {
		ref, vhost string
		want       bool
	}{
		{"", "example.org", false},
		{"-", "example.org", false},
		{"not a url", "example.org", false},
		{"http://example.org/self", "example.org", false},
		{"http://sub.example.org/self", "example.org", false},
		{"http://example.org:443/self", "example.org:443", false},
		{"http://EXAMPLE.org./self", "example.org:443", false},
		{"http://[2001:0db8::1]/self", "[2001:db8::1]:443", false},
		{"http://[2001:db8::1]/self", "2001:db8::1", false},
		{"http://[2001:db8::2]/external", "[2001:db8::1]:443", true},
		{"http://other.org/x", "example.org", true},
		{"http://evil.example.org.evil.com/x", "example.org", true},
		{"http://anywhere.org/x", "", true},
		{"", "", false},
	}
	for _, c := range cases {
		if got := RefererExternal(c.ref, c.vhost); got != c.want {
			t.Errorf("RefererExternal(%q,%q) = %t, want %t", c.ref, c.vhost, got, c.want)
		}
	}
}
