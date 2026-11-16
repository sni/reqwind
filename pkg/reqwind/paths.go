package reqwind

import (
	"fmt"
	"html"
	"html/template"
	"maps"
	"slices"
	"strings"
)

const (
	otherLabel = "(other)"
	// shortcut: cap nested HTML at 500 levels; render on demand if real logs need deeper paths.
	maxTreeDepth = 500
	// Small path leaves are grouped away so the tree stays usable when the
	// report retains more than this many distinct path labels.
	minLeafRequests = 10
	uniquePathLimit = 1000
	// The tree renders at most this many rows; the remainder is folded into
	// one grouped row per parent. High-cardinality hosts (for example one
	// row per versioned download) would otherwise produce multi-megabyte
	// pages from long-tail paths.
	maxTreeRows = 5000
)

// pathRender is the shared row budget of one drilldown render.
type pathRender struct {
	remaining int
}

// Folder (expandable prefix) and file (leaf) icons are CSS masks defined once
// in the stylesheet; each row emits only the span so the tree does not repeat
// the SVG markup per entry. The mask uses currentColor, so the icons follow
// the palette.
const (
	folderIcon = `<span class="pi pi-folder"></span>`
	fileIcon   = `<span class="pi pi-file"></span>`
)

type pathNode struct {
	own, sub int64 // requests exactly here / on all descendants (sub excl. own)
	kids     map[string]*pathNode
}

// drilldownPaths excludes failed (4xx/5xx) and redirect (3xx) requests so
// error and redirect noise does not rank paths.
func drilldownPaths(paths, errors, redirects map[string]int64) map[string]int64 {
	filtered := make(map[string]int64, len(paths))
	for path, count := range paths {
		count -= errors[path] + redirects[path]
		if count > 0 {
			filtered[path] = count
		}
	}
	return filtered
}

// Path labels are untrusted; escape them before building the expandable tree.
func pathDrilldown(paths map[string]int64) template.HTML {
	if len(paths) == 0 {
		return template.HTML(`<p class="empty">No path data.</p>`)
	}
	root, flat := buildPathTree(paths)
	// Group tiny leaves only when the report keeps many distinct labels;
	// small reports stay fully expanded.
	collapse := len(paths) > uniquePathLimit
	var b strings.Builder
	if _, ok := paths[otherLabel]; ok {
		fmt.Fprintf(&b, `<p class="meta">%s groups paths beyond the storage limit and is a label, not a URL prefix.</p>`, otherLabel)
	}
	b.WriteString(`<div class="path-tree"><h3>Prefix drilldown</h3><div class="path-row path-header"><span>Path</span><span class="path-counts"><span>Exact</span><span>Total</span><span class="bar" aria-hidden="true"></span></span></div><details open><summary>`)
	total := root.own + root.sub
	pr := &pathRender{remaining: maxTreeRows}
	fmt.Fprintf(&b, `<span class="path-row">%s/%s</span></summary><ul>`, folderIcon, pathCounts(root.own, total, total))
	pr.remaining--
	var rootLeft int64
	keys := slices.Sorted(maps.Keys(root.kids))
	for i, name := range keys {
		if pr.remaining <= 0 {
			for _, q := range keys[i:] {
				rootLeft += root.kids[q].own + root.kids[q].sub
			}
			break
		}
		renderPathNode(&b, name, root.kids[name], 1, total, collapse, pr)
	}
	if rootLeft > 0 {
		fmt.Fprintf(&b, "\n"+`<li><span class="path-row">%s<span class="meta">…</span>%s</span> <span class="meta">further paths, grouped</span></li>`, fileIcon, countSpan(rootLeft))
	}
	b.WriteString(`</ul></details>`)
	if len(flat) > 0 {
		b.WriteString(`<details><summary>paths without leading slash</summary><ul>`)
		var left int64
		flatKeys := slices.Sorted(maps.Keys(flat))
		for i, p := range flatKeys {
			if pr.remaining <= 0 {
				for _, q := range flatKeys[i:] {
					left += flat[q]
				}
				break
			}
			fmt.Fprintf(&b, "\n"+`<li><span class="path-row">%s%s%s</span></li>`, fileIcon, html.EscapeString(p), countSpan(flat[p]))
			pr.remaining--
		}
		if left > 0 {
			fmt.Fprintf(&b, "\n"+`<li><span class="path-row">%s%s%s</span> <span class="meta">further paths without leading slash, grouped</span></li>`, fileIcon, otherLabel, countSpan(left))
		}
		b.WriteString(`</ul></details>`)
	}
	b.WriteString(`</div>`)
	return template.HTML(b.String())
}

// buildPathTree groups all path counts into a trie. Subtree totals are
// accumulated here (never by recursion), so pathological depth costs O(n)
// per path but no stack. Paths that do not start with "/" have no segment
// prefix and are returned in flat instead.
func buildPathTree(paths map[string]int64) (root *pathNode, flat map[string]int64) {
	root = &pathNode{kids: map[string]*pathNode{}}
	flat = map[string]int64{}
	for p, c := range paths {
		switch {
		case p == otherLabel:
			// label only, no prefix semantics
		case !strings.HasPrefix(p, "/"):
			flat[p] += c
		case p == "/":
			root.own += c
		default:
			n := root
			for _, seg := range strings.Split(p[1:], "/") {
				n.sub += c
				if n.kids[seg] == nil {
					n.kids[seg] = &pathNode{kids: map[string]*pathNode{}}
				}
				n = n.kids[seg]
			}
			n.own += c
		}
	}
	return root, flat
}

// renderPathNode appends one <li> (leaf) or <details> (branch) entry at the
// given nesting depth and stops with a marker at maxTreeDepth. The row budget
// pr folds the tail of the tree into one grouped row per parent so
// high-cardinality paths cannot bloat the page.
func renderPathNode(b *strings.Builder, name string, n *pathNode, depth int, total int64, collapse bool, pr *pathRender) {
	if depth > maxTreeDepth {
		// Marker shows the inclusive total so nothing below the cap
		// is invisible; the cap keeps stack depth and output bounded.
		fmt.Fprintf(b, "\n"+`<li><span class="path-row">%s<span>%s</span>%s</span> <span class="meta">deeper than %d levels; total shown, not expanded</span></li>`, folderIcon, renderSegment(name), pathCounts(n.own, n.own+n.sub, total), maxTreeDepth)
		pr.remaining--
		return
	}
	if len(n.kids) == 0 {
		fmt.Fprintf(b, "\n"+`<li><span class="path-row">%s<span>%s</span>%s</span></li>`, fileIcon, renderSegment(name), pathCounts(n.own, n.own, total))
		pr.remaining--
		return
	}
	names := slices.Sorted(maps.Keys(n.kids))
	fmt.Fprintf(b, "\n"+`<li><details><summary><span class="path-row">%s<span>%s</span>%s</span></summary><ul>`, folderIcon, renderSegment(name), pathCounts(n.own, n.own+n.sub, total))
	pr.remaining--
	var groupCount int64 // sum of collapsed leaves under this parent
	var left int64       // sum of requests folded away by the row budget
	// The group row is one sibling per parent, written after the last child
	// so several interleaved tiny leaves do not produce several rows.
	for i, k := range names {
		kid := n.kids[k]
		// Collapse small leaves, but keep the last child visible so a group
		// row never hides a parent's only entry.
		if collapse && len(kid.kids) == 0 && kid.own < minLeafRequests && i < len(names)-1 {
			groupCount += kid.own
			continue
		}
		if pr.remaining <= 0 {
			for _, q := range names[i:] {
				left += n.kids[q].own + n.kids[q].sub
			}
			break
		}
		renderPathNode(b, k, kid, depth+1, total, collapse, pr)
	}
	if groupCount > 0 {
		fmt.Fprintf(b, "\n"+`<li><span class="path-row">%s%s%s</span> <span class="meta">paths with fewer than %d requests, grouped</span></li>`, fileIcon, otherLabel, countSpan(groupCount), minLeafRequests)
	}
	if left > 0 {
		fmt.Fprintf(b, "\n"+`<li><span class="path-row">%s<span class="meta">…</span>%s</span> <span class="meta">further paths, grouped</span></li>`, fileIcon, countSpan(left))
	}
	b.WriteString(`</ul></details></li>`)
}

// renderSegment escapes a path segment, showing a visible label for the
// raw empty segment (trailing slash).
func renderSegment(seg string) string {
	if seg == "" {
		return `<span class="meta">/ (empty segment)</span>`
	}
	return html.EscapeString(seg)
}

func countSpan(c int64) string {
	return fmt.Sprintf(`<span class="n">%s</span>`, group(c))
}

func pathCounts(own, count, total int64) string {
	percent := float64(0)
	if total > 0 {
		percent = float64(count) / float64(total) * 100
	}
	// Fixed-width value cells keep the right edge aligned across every row.
	// The values are visible in the cells, so no redundant title attribute.
	// The bar div has no static aria-label; a small script in the report
	// sets it on every meter from its value, keeping each row short.
	return fmt.Sprintf(`<span class="path-counts"><span>%s</span><span>%s</span><meter min="0" max="100" value="%.2f">%.1f%%</meter></span>`, group(own), group(count), percent, percent)
}
