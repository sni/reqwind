package reqwind

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestDrilldownFiltersFailuresAndRedirects(t *testing.T) {
	paths := map[string]int64{"/ok": 20, "/mixed": 5, "/rare": 1, "/frequent": 10000, "/redirected": 4}
	errors := map[string]int64{"/mixed": 2, "/rare": 1, "/frequent": 10000}
	redirects := map[string]int64{"/redirected": 4, "/mixed": 2}
	want := map[string]int64{"/ok": 20, "/mixed": 1}
	if got := drilldownPaths(paths, errors, redirects); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if paths["/mixed"] != 5 {
		t.Fatal("filter changed the source totals")
	}
	// A path with only redirects drops out entirely; without redirect
	// counts (older state) it stays.
	if got := drilldownPaths(paths, errors, nil); !reflect.DeepEqual(got, map[string]int64{"/ok": 20, "/mixed": 3, "/redirected": 4}) {
		t.Errorf("missing redirect counts must not hide paths: %v", got)
	}
}

func TestPathDrilldownEmpty(t *testing.T) {
	if got := pathDrilldown(nil); !strings.Contains(string(got), `class="empty"`) {
		t.Fatalf("empty input should render the empty marker, got %q", got)
	}
}

func TestPathPercentages(t *testing.T) {
	out := string(pathDrilldown(map[string]int64{"/a": 1, "/b": 3, otherLabel: 20}))
	// The meter stays compact: no aria-label in the HTML (a script adds it).
	for _, value := range []string{`value="100.00"`, `value="25.00"`, `value="75.00"`, `class="path-counts"`, `<span class="path-counts"><span>1</span><span>1</span><meter min="0" max="100" value="25.00">25.0%</meter></span>`} {
		if !strings.Contains(out, value) {
			t.Errorf("missing %s", value)
		}
	}
	if strings.Contains(out, `meter min="0" max="100" value="25.00" aria-label`) {
		t.Errorf("meter carries a static aria-label: %q", out)
	}
	if out := string(pathDrilldown(map[string]int64{"/": 0})); strings.Contains(out, "NaN") || strings.Contains(out, "+Inf") {
		t.Fatal("zero requests produced an invalid percentage")
	}
}

func TestPathDrilldownEscapes(t *testing.T) {
	out := string(pathDrilldown(map[string]int64{
		`<script>alert(1)</script>`:        1,
		`/a"><img src=x onerror=alert(2)>`: 1,
	}))
	for _, banned := range []string{`<script`, `<img`} {
		if strings.Contains(out, banned) {
			t.Errorf("output contains live %q", banned)
		}
	}
	if !strings.Contains(out, `&lt;script&gt;`) || !strings.Contains(out, `&lt;img src=x onerror=alert(2)&gt;`) {
		t.Errorf("untrusted paths not escaped: %q", out)
	}
}

func TestPathDrilldownTotals(t *testing.T) {
	out := string(pathDrilldown(map[string]int64{
		"/":        1,
		"/a":       3,
		"/a/b":     5,
		"/a/b/c":   7,
		"/b":       9,
		"/b/c/d":   11,
		"/b/c/d/e": 13,
	}))
	// Counts sit in position-ordered spans; pair each exact value with its
	// total via the shared counts cell.
	for _, pr := range [][2]string{{"1", "49"}, {"3", "15"}, {"5", "12"}, {"9", "33"}} {
		want := `<span class="path-counts"><span>` + pr[0] + `</span><span>` + pr[1] + `</span>`
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	if !strings.Contains(out, `<details open>`) || strings.Contains(out, "<table") {
		t.Error("the first tree level must be open, without a depth table")
	}
}

func TestPathDrilldownOtherLabel(t *testing.T) {
	out := string(pathDrilldown(map[string]int64{
		"/":       1,
		"/a":      2,
		"(other)": 100,
	}))
	// The (other) label is not a URL prefix.
	if !strings.Contains(out, `<span>3</span>`) {
		t.Errorf("(other) leaked into prefix totals: %q", out)
	}
	if strings.Contains(out, `100 requests`) {
		t.Errorf("(other) count leaked into drilldown output")
	}
	if !strings.Contains(out, "(other) groups paths beyond the storage limit") {
		t.Errorf("(other) explanation missing")
	}
}

// Trailing-slash paths map to a raw empty segment; the label "(empty)"
// must not collide with a real "/a/(empty)" path.
func TestPathDrilldownEmptySegment(t *testing.T) {
	out := string(pathDrilldown(map[string]int64{
		"/a/":        2,
		"/a":         3,
		"/a/(empty)": 4,
	}))
	// /a total is 3+2+4=9; the two children are distinct nodes.
	if !strings.Contains(out, `<span>9</span>`) {
		t.Fatalf("/a total wrong: %q", out)
	}
	for _, want := range []string{`/ (empty segment)`, `(empty)`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	// Children render as leaves with their own counts only.
	if !strings.Contains(out, `<span>2</span>`) ||
		!strings.Contains(out, `<span>4</span>`) {
		t.Errorf("children of /a not shown distinctly: %q", out)
	}
	// The two children render under their distinct wrapped labels.
	if !strings.Contains(out, `<span><span class="meta">/ (empty segment)</span></span>`) || !strings.Contains(out, `<span>(empty)</span>`) {
		t.Errorf("child labels missing: %q", out)
	}
}

func TestPathDrilldownDeterministic(t *testing.T) {
	paths := map[string]int64{
		"/z": 1, "/a": 2, "/a/b": 3, "/a/a": 4, "/m": 5, "/m/a": 6,
		"/": 7, "no-slash": 8, "other-flat": 9, "/deep/x/y/z": 10,
	}
	first := string(pathDrilldown(paths))
	for i := 0; i < 50; i++ {
		if got := string(pathDrilldown(paths)); got != first {
			t.Fatalf("render %d differs from the first render", i)
		}
	}
	// Root children are alphabetical, not by count. Anchor on the wrapped
	// segment names so the leading icon does not matter. The /z leaf comes
	// after /m; the earlier z is inside /deep.
	// a, deep and m are root children in order; z is present (a z also nests
	// under /deep, so only a < deep < m is asserted).
	a := strings.Index(first, `<span>a</span>`)
	deep := strings.Index(first, `<span>deep</span>`)
	m := strings.Index(first, `<span>m</span>`)
	z := strings.Index(first, `<span>z</span>`)
	if a < 0 || deep < 0 || m < 0 || z < 0 || !(a < deep && deep < m) {
		t.Errorf("root children not in alphabetical order (a=%d deep=%d m=%d z=%d)", a, deep, m, z)
	}
}

func TestPathDrilldownPathologicalDepth(t *testing.T) {
	// The parser allows up to 1 MiB paths; this must not crash or hang.
	segs := strings.Repeat("/x", 200000)
	out := string(pathDrilldown(map[string]int64{"/" + segs: 1}))
	if !strings.Contains(out, "deeper than 500 levels; total shown, not expanded") {
		t.Errorf("depth cap marker missing: %q", out)
	}
	if !strings.Contains(out, `<span>1</span>`) {
		t.Errorf("total under the cap lost: %q", out)
	}
}

func TestPathDrilldownCollapsesSmallLeaves(t *testing.T) {
	// More than uniquePathLimit labels: leaves below the cap are grouped.
	paths := map[string]int64{"/kept": 20}
	for i := 0; i < 1001; i++ {
		paths[fmt.Sprintf("/tiny/%d", i)] = 1
	}
	// A big leaf between the small ones: the group row must not split around it.
	paths["/tiny/500x"] = 100
	out := string(pathDrilldown(paths))
	if !strings.Contains(out, "paths with fewer than 10 requests, grouped") {
		t.Errorf("grouped marker missing: %q", out)
	}
	// Every collapsed leaf had exactly one request; only the last child
	// (999) may stay visible with its 1/1 count cell.
	if n := strings.Count(out, `<span class="path-counts"><span>1</span><span>1</span>`); n != 1 {
		t.Errorf("small leaves not collapsed: %d single-request cells, want 1", n)
	}
	// The grouped total carries every collapsed request (leaves 0-999).
	if !strings.Contains(out, `>1,000</span>`) {
		t.Errorf("grouped count wrong: %q", out)
	}
	// "999" sorts last lexicographically and stays visible, as does the
	// interleaved big leaf "500x".
	for _, seg := range []string{`<span>999</span>`, `<span>500x</span>`} {
		if !strings.Contains(out, seg) {
			t.Errorf("expected visible leaf missing: %q", out)
		}
	}
	// One group row per parent, not one per run of tiny leaves.
	if n := strings.Count(out, "paths with fewer than 10 requests, grouped"); n != 1 {
		t.Errorf("group rows = %d, want 1", n)
	}
	// A small report stays fully expanded.
	small := map[string]int64{"/tiny/a": 1, "/tiny/b": 2}
	if out := string(pathDrilldown(small)); strings.Contains(out, "grouped") {
		t.Errorf("small report should not group: %q", out)
	}
}

// TestPathDrilldownCapsRows guards against high-cardinality hosts (one row
// per versioned download) producing multi-megabyte pages.
func TestPathDrilldownCapsRows(t *testing.T) {
	// Counts stay above the small-leaf threshold so only the row cap applies.
	paths := map[string]int64{"/": 1}
	for i := 0; i < 6000; i++ {
		paths[fmt.Sprintf("/pkg-%d.rpm", i)] = int64(i + 10)
	}
	out := string(pathDrilldown(paths))
	// Data rows are the rendered list items; the budget bounds them.
	if rows := strings.Count(out, `</li>`); rows > maxTreeRows {
		t.Fatalf("drilldown has %d data rows, cap is %d", rows, maxTreeRows)
	}
	// The folded tail keeps the real total of every hidden path. Each rendered
	// child (and the root row) consumes one of the budget; the remaining
	// children (lexicographic order) fold into one grouped row.
	if !strings.Contains(out, "further paths, grouped") {
		t.Fatal("grouped tail missing")
	}
	keys := make([]string, 0, 6000)
	for i := 0; i < 6000; i++ {
		keys = append(keys, fmt.Sprintf("/pkg-%d.rpm", i))
	}
	sort.Strings(keys)
	hidden := int64(0)
	for _, k := range keys[maxTreeRows-1:] { // root + (cap-1) children render
		hidden += paths[k]
	}
	if !strings.Contains(out, fmt.Sprintf(`>%s</span>`, group(hidden))) {
		t.Errorf("grouped tail total wrong, want %s", group(hidden))
	}
}

func TestPathDrilldownNonSlash(t *testing.T) {
	out := string(pathDrilldown(map[string]int64{
		"/a":       3,
		"no-slash": 4,
	}))
	if !strings.Contains(out, "paths without leading slash") || !strings.Contains(out, `>4</span>`) {
		t.Errorf("flat path count missing: %q", out)
	}
	if !strings.Contains(out, "no-slash") {
		t.Errorf("flat path missing")
	}
}
