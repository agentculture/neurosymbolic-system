// Package allowlist enforces import hygiene for the neurosymbolic-engine,
// rejecting packages outside the stdlib and this module unless explicitly
// allowlisted in go.mod.
//
// The guard ensures the engine stays a pure library without hard dependencies
// on robot SDKs, transports, media systems, or subprocess/plugin machinery.
// See docs/go-dependencies.md for the policy and denylist.
package allowlist
