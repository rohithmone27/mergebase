// Package web embeds the built frontend. web/dist is produced by `npm run
// build` (the Docker build does this automatically); the committed
// placeholder index.html keeps `go build` working before the first UI build.
package web

import "embed"

//go:embed all:dist
var Dist embed.FS
