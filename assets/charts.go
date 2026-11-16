package assets

import _ "embed"

// EChartsJS is the bundled Apache ECharts library, version 5.6.0 (Apache-2.0).
// Byte-identical to npm package echarts@5.6.0, file dist/echarts.min.js.
//
//go:embed echarts.min.js
var EChartsJS []byte

// EChartsLicense is the full Apache ECharts LICENSE text from the official
// 5.6.0 release (raw.githubusercontent.com/apache/echarts/5.6.0/LICENSE),
// including the Apache 2.0 body and the subcomponent notices (d3.js, etc.).
//
//go:embed LICENSE
var EChartsLicense []byte

// EChartsNotice is the Apache ECharts NOTICE text from the official 5.6.0
// release (raw.githubusercontent.com/apache/echarts/5.6.0/NOTICE).
//
//go:embed NOTICE
var EChartsNotice []byte

// WorldGeoJSON is the bundled world map geometry (GeoJSON).
// Byte-identical to npm package echarts@4.9.0, file map/json/world.json,
// distributed under the Apache License 2.0.
//
//go:embed world.geo.json
var WorldGeoJSON []byte

//go:embed SOURCES.txt
var Sources []byte
