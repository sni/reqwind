package reqwind

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A manifest value is "digest|version|generated-rfc3339". Values without
// separators are legacy plain digests and never skip a rewrite.
func splitManifest(entry string) (digest, version string, generated time.Time, ok bool) {
	digest, rest, found := strings.Cut(entry, "|")
	if !found {
		return digest, "", time.Time{}, false
	}
	version, stamp, found := strings.Cut(rest, "|")
	if !found {
		return digest, "", time.Time{}, false
	}
	generated, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return digest, "", time.Time{}, false
	}
	return digest, version, generated, true
}

func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeReports(s *State, statePath, index string, pages []Page, inputs []string, out io.Writer) error {
	if s.Outputs == nil {
		s.Outputs = map[string]map[string]string{}
	}
	// Ctrl+C (SIGINT) is captured, not fatal, so stop promptly at the next
	// page boundary: rendering is the slow phase and must not ignore the signal.
	stopped := func() error {
		if s.ctx != nil {
			return s.ctx.Err()
		}
		return nil
	}
	previous := s.Outputs[index]
	manifest := map[string]string{}
	for path, entry := range previous {
		manifest[path] = entry
	}
	current := map[string]bool{}
	skip := map[string]bool{}
	for _, page := range pages {
		if err := stopped(); err != nil {
			return err
		}
		// Re-render with the stored timestamp: unchanged data with the same
		// reqwind version gives an identical digest, so the page keeps its
		// stored timestamp and is not rewritten. A version change never
		// matches, so every page rebuilds.
		if digest, version, generated, ok := splitManifest(previous[page.Path]); ok && version == Version {
			r := page.Report
			r.Generated = generated
			h := sha256.New()
			if err := RenderReport(h, r); err != nil {
				return err
			}
			if hex.EncodeToString(h.Sum(nil)) == digest {
				if got, err := fileDigest(page.Path); err == nil && got == digest {
					current[page.Path] = true
					skip[page.Path] = true
					continue
				}
			}
		}
		h := sha256.New()
		if err := RenderReport(h, page.Report); err != nil {
			return err
		}
		manifest[page.Path] = hex.EncodeToString(h.Sum(nil)) + "|" + Version + "|" + page.Report.Generated.UTC().Format(time.RFC3339)
		current[page.Path] = true
	}
	targets := []string{statePath, statePath + ".lock"}
	for _, p := range pages {
		if p.Report.ExternalECharts {
			// The shared asset files are outputs; a log may not alias them.
			targets = append(targets,
				filepath.Join(filepath.Dir(index), echartsAssetName),
				filepath.Join(filepath.Dir(index), mapAssetName))
			break
		}
	}
	for path := range manifest {
		targets = append(targets, path)
	}
	if err := checkTargets(inputs, targets); err != nil {
		return err
	}
	s.Outputs[index] = manifest
	// Commit the state (meta + month buckets) before publishing any pages.
	if err := stopped(); err != nil {
		return err
	}
	if err := s.saveState(statePath); err != nil {
		return err
	}
	for i := len(pages) - 1; i >= 0; i-- {
		if err := stopped(); err != nil {
			return err
		}
		page := pages[i]
		if skip[page.Path] {
			continue
		}
		if err := atomicWrite(page.Path, 0644, func(w io.Writer) error { return RenderReport(w, page.Report) }); err != nil {
			return err
		}
	}
	// Expired pages are never deleted; the manifest keeps their hashes so
	// later runs can still warn about modified expired content.
	for path, entry := range previous {
		if current[path] {
			continue
		}
		digest, _, _, _ := splitManifest(entry)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			fmt.Fprintf(out, "reqwind: keeping changed expired path %s; remove it manually if it contains old data\n", path)
			continue
		}
		got, err := fileDigest(path)
		if err != nil {
			return err
		}
		if got != digest {
			fmt.Fprintf(out, "reqwind: keeping modified expired page %s; remove it manually if it contains old data\n", path)
			continue
		}
	}
	return nil
}
