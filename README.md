# reqwind

![reqwind logo](assets/logo.svg)

reqwind is an Apache/nginx access-log analyzer written in Go. One binary reads
multiple logs and writes a static HTML index with year, month, and vhost
reports. Each page carries its own styles; the chart library (Apache ECharts)
and world map load from two shared files (`echarts.min.js`,
`world.geo.json`) written beside the index. `-inline-assets` embeds them into
every page instead. No database, server, or runtime network requests.

## Build and run

Requires Go 1.23+ on Linux.

Prebuilt Linux binaries are attached to GitHub Releases. Choose `amd64`
(x86-64) or `arm64` (ARM64). Download and unpack the archive.
Replace `<version>` and `<arch>` below:

```sh
tar -xzf reqwind-<version>-linux-<arch>.tar.gz
sudo install -m 0755 reqwind /usr/local/bin/reqwind
```

The binaries need no C runtime. To build from source:

```sh
make
# Without make: go build -o reqwind ./cmd/reqwind
./reqwind /var/log/httpd/access_log
```

Open `index.html` or publish the generated files with your static web server.

```sh
# Write a named report, keep state elsewhere, poll live
./reqwind -output public/stats.html -state private/stats -live '/var/log/nginx/*access.log'

# Assign hosts to specific logs
./reqwind -vhost example.org=/var/log/nginx/example-access.log

# Other flags
./reqwind -verbose -palette forest access.log access.log.1.gz
./reqwind -geo-db /usr/share/GeoIP/GeoLite2-Country.mmdb access.log
./reqwind -hash-clients access.log
# Embed the chart library and world map into every page instead of shared files
./reqwind -inline-assets access.log
./reqwind -merge-state machine-a -merge-state machine-b -output combined/index.html
./reqwind -help
./reqwind -version
```

Create output and state parent directories first. With no log arguments,
reqwind discovers standard Apache/nginx access logs, rotations, and `.gz`
archives under `/var/log/nginx`, `/var/log/apache2`, and `/var/log/httpd`.
Explicit input paths limit ingestion to those paths. Flags precede log paths.

Defaults: `index.html`, `.reqwind` state file, and two-second live polling.
Ingestion and daily totals use UTC. Set `TZ` for dashboard timestamps and
hourly chart labels. Every run prints a short summary of new, invalid, and
already-read lines. `-verbose` reports more, including invalid lines and one
line per written file; those can contain client addresses or secrets, so keep
verbose output private.

## State and retention

The state is a small meta file at the `-state` path plus one bucket file per
month beside it, named `<base>-YYYY-MM.json`. The meta file holds file
checkpoints, session intervals, and the published-page manifest. Each bucket
holds that month's aggregates. Both files contain gzip-compressed JSON, despite
their unchanged names. Use `gzip -dc <file>` to inspect them.

reqwind never deletes month data or generated pages on its own. Stop live mode
before removing them by hand. Session
updates do not recreate removed months; new log records for those months do.

Reports link to year, month, and vhost pages beside the index. Publish the
pages together. Each page works without remote assets. Charts require
JavaScript; tables and path drilldowns stay readable without it. The all-sites
index shows only the trailing 12 months, ending with the latest data month,
except for its yearly summary, which keeps the full history. Detail pages
keep their full date range. Summary tables label host names as “Virtual host”
and years or months as “Period”. Path drilldowns render at most 5000 rows; the
remaining paths fold into one grouped row that keeps their combined total.
Large reports (more than 1000 distinct paths) also fold path leaves with
fewer than 10 requests into a single `(other)` row per parent folder.
Pages that hold more than one virtual host or log source add two extra
charts: requests and traffic per period by virtual host.
The per-day totals table is omitted on the index
and on all-sites yearly reports, where a 365-row table adds little. By
default the pages load the chart library and world map from two shared files
(`echarts.min.js` and `world.geo.json`) written beside the index; publish both
with the pages. Serving those files over HTTP lets the world map load (a
browser blocks the local fetch when the page is opened straight from disk);
the charts and all tables still work either way. `-inline-assets` embeds both
into every page so the pages open from disk.

## Supported logs

Apache and nginx **common** and **combined** access logs, IPv4/IPv6 clients,
and optional virtual-host prefixes (bare or bracketed). Each file can use a
different supported format. Malformed and oversized lines are counted and
skipped. Gzip archives stream with concatenated-member support; unchanged
archives are skipped via checkpoints.

Vhost prefixes separate hosts automatically, even within one file. Ports in
log prefixes are ignored. Use repeatable `-vhost hostname=path-or-glob`
mappings to assign requests from specific files to a given name; the mapping
overrides log prefixes.

## Key metrics

| Metric | Definition |
| --- | --- |
| Hits | All valid requests in the stored months. |
| Files | Successful 2xx responses with a nonzero body byte count. |
| Pages | Successful 2xx requests for page-like paths, not assets or downloads. |
| Visits | Page-hit sessions per client address and vhost, split by more than 30 minutes of inactivity. |
| Visitors | Distinct hashed client addresses, not people. |
| Traffic | Sum of logged response-body bytes. |

Sessions can cross midnight and file boundaries and stay correct when logs
arrive out of order. IP-based metrics cannot distinguish people behind NAT and
may split one person across addresses. Bots are not excluded.

“Top error paths” counts 4xx and 5xx responses except 401. All requests still
count toward request and status totals. To exclude 401s from older saved
counts, rebuild the state from the source logs with a new `-state` path.

## Reports from saved state files

Repeatable `-merge-state path-or-glob` flags combine saved state meta files
from several machines. No source logs are needed. This mode reads snapshots
and writes HTML only; it does not change source state or run geolocation.
Snapshots must cover separate requests; overlapping snapshots count requests
twice.

## Local geolocation

Geolocation is optional and stays offline. reqwind looks for local GeoLite2
or legacy GeoIP databases in standard directories and uses the installed
`mmdblookup`, `geoiplookup`, or `geoiplookup6` command. No database is
downloaded and no address is sent to a remote service. Without a usable
database, analysis continues without countries.

The lookup tool and database are separate installs. On Debian, install the
`geoip-database` package and its matching lookup tools:

```sh
sudo apt install geoip-database geoip-bin
```

Reqwind detects the packaged `/usr/share/GeoIP/GeoIP.dat` automatically. On
Rocky Linux/RHEL, install `geolite2-country` for the database and
`libmaxminddb` for `mmdblookup`:

```sh
sudo dnf install geolite2-country libmaxminddb
```

Reqwind detects `/usr/share/GeoIP/GeoLite2-Country.mmdb` automatically.
`-geo-db /path/to/database` selects an explicit database and fails clearly if
it cannot be used; `-geo-db off` disables lookups. Countries are estimates.
Private-range and unresolvable addresses have no country. Reqwind records
private-range visitors under `Local/private` and the rest under `Unknown`.
Both appear in the country tables and the per-country visitor totals, but not
on the world map (there is no polygon to place them).

## Incremental reads

reqwind reads only new bytes since the last run. Plain-text files use byte
checkpoints; gzip files use decoded offsets. Rename rotation is tracked by
device/inode, so a rotated file is not counted twice. Unchanged reruns read
zero new log bytes and rewrite no files, except when upgrading plain JSON state
to compressed state: pages and the state files keep their stored bytes when
nothing changed. A new reqwind version rebuilds every page. Shared static assets
are written only when missing or changed.
Writes use a temporary file, sync, and atomic rename; a process
lock prevents simultaneous writers to the same state.

## Privacy

Raw client IPs are stored and shown by default. Use `-hash-clients` to store
and show per-state HMAC identifiers instead. Search terms come only from
recognized search-engine referrers and can contain personal data. Keep state
files **outside your web root** (mode `0600`) and control access to the
reports.

## Verify

```sh
make check
make benchmark
```

Tests cover parsing, incremental reruns, gzip handling, retention, saved-state
reports, out-of-order visits, geolocation, client rankings, and live refresh.
Chart theme tests use Node.js when available; Node.js is not needed to build
or run reqwind.

`BenchmarkStateJSON` measures JSON encoding, decoding, and gzip levels on a
sample bucket. The fast gzip level cut its size by 74% and took about half
the time of the default level in local tests. Results depend on the data.
The project keeps Go's JSON library. In a separate comparison on local state,
`goccy/go-json` v0.12.0 cut the largest bucket's JSON time from about 1.2 to
0.65 ms for encoding and 1.5 to 0.87 ms for decoding, but used more memory
for decoding. These small savings do not yet justify a new dependency.

## CI and releases

See [CHANGELOG.md](CHANGELOG.md) for changes.

GitHub Actions runs tests with the race detector, vet, formatting checks,
lint, vulnerability checks, and benchmarks on pushes and pull requests.
It also builds Linux `amd64` and `arm64` archives.
Download these from the workflow's artifacts section. You can also start
the Builds workflow by hand.

Dependabot checks GitHub Actions daily at 04:00 UTC and Go modules weekly
on Monday at 10:00 UTC. It opens pull requests for updates; merges stay manual.

To prepare a release, tag a commit and push the tag:

```sh
git tag v0.0.1
git push origin v0.0.1
```

Use the next release version in place of `v0.0.1`. After all checks and
builds pass, the workflow creates a **draft** release with generated notes
and binary archives. Review the notes and publish it on GitHub.
The workflow never publishes a release on its own. It uses `GITHUB_TOKEN`;
no extra secrets are needed. Repository policy must allow write access to
release contents.

Tagged binaries report the tag version with `-version`. Other builds use
the nearest version tag plus the commit distance and ID, or just the commit
ID if no version tag exists.

## License

reqwind is MIT licensed; see `LICENSE`. Bundled Apache ECharts and map data
retain their own Apache-2.0 license and notices in `assets/LICENSE` and
`assets/NOTICE`. `assets/SOURCES.txt` lists their sources.
