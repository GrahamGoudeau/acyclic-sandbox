// Package web holds the playground's static files.
package web

import "embed"

// FS is the playground: index.html, app.js, snippets.js, style.css and a
// vendored copy of acyclic-sdk-js under sdk/ (scripts/sync-sdk.sh).
//
//go:embed *.html *.js *.css sdk
var FS embed.FS
