// Package internal is a placeholder for the neurosymbolic-system Go engine's
// non-exported packages.
//
// Nothing lives here yet. t1 (go module scaffold and CI) only establishes
// the layout — go.mod, cmd/neurosymbolic-engine, and this directory — so
// that later tasks have a place to put the engine's internals (senses,
// arbitration, rules, motion; see CLAUDE.md's "The runtime being extracted")
// without a restructuring PR first. Each subpackage that lands here should
// replace this file's role with its own package doc, the way
// cmd/neurosymbolic-engine/main.go documents the entry point.
package internal
