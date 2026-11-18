// Package web embeds the promui frontend assets (templates and static
// files) so the binary ships as a single artifact.
package web

import "embed"

// Content holds the page templates and static assets of the UI.
//
//go:embed *.html static/*
var Content embed.FS
