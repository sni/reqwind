package reqwind

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
)

var defaultLogBases = []string{
	"/var/log/nginx/access.log",
	"/var/log/nginx/access_log",
	"/var/log/apache2/access.log",
	"/var/log/apache2/other_vhosts_access.log",
	"/var/log/httpd/access_log",
	"/var/log/httpd/access.log",
	"/var/log/httpd/ssl_access_log",
}

func discoverLogs(bases []string) ([]string, error) {
	seen := map[string]bool{}
	var paths []string
	for _, base := range bases {
		for _, dir := range []string{filepath.Dir(base), filepath.Join(filepath.Dir(base), "archive"), filepath.Join(filepath.Dir(base), "old")} {
			pattern := filepath.Join(dir, filepath.Base(base)) + "*"
			matches, err := filepath.Glob(pattern)
			if err != nil {
				return nil, err
			}
			for _, path := range matches {
				if rotationBase(path) != filepath.Join(dir, filepath.Base(base)) {
					continue
				}
				info, err := os.Stat(path)
				if err != nil {
					return nil, err
				}
				if !info.Mode().IsRegular() || seen[path] {
					continue
				}
				seen[path] = true
				paths = append(paths, path)
			}
		}
	}
	if len(paths) == 0 {
		return nil, errors.New("no default access logs or archives found; pass log paths")
	}
	sort.Strings(paths)
	return paths, nil
}
