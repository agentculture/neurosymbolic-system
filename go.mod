module github.com/agentculture/neurosymbolic-system

go 1.26

// The runtime's dependency policy is empty-or-near-empty: a library imported by
// two robot CLIs must install everywhere, so anything beyond the stdlib carries
// its argument next to the pin.
require github.com/BurntSushi/toml v1.6.0 // allow: pure-Go TOML decoder, no transitive deps; both donors' overlays are TOML (frame c3)
