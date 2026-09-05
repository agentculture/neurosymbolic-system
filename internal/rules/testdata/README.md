# `internal/rules/testdata` — the donors' shipped rule files

Both robots' `default_rules.toml` are copied here **verbatim**. They are the
loader's contract: whatever this package does, it must keep loading the rules
two real robots ship today.

| File | Source | Notes |
|---|---|---|
| `reachy/default_rules.toml` | `reachy-mini-cli/reachy/behavior/default_rules.toml` | verbatim; **no `schema_version` line** — the donor predates the field, so this copy is REFUSED fail-closed |
| `reachy/default_rules.v1.toml` | the same file | with `schema_version = 1` prepended (and a header saying so) — this is the copy that loads |
| `microduck/default_rules.toml` | `microduck-cli/microduck_cli/behavior/default_rules.toml` | verbatim; already carries `schema_version = 1` |

Re-sync by copying the donor file again. `default_rules.v1.toml` is the reachy
copy plus its four-line header — regenerate it the same way, never by editing
the rules themselves.

The rule ids in these files are a **public interface**: an operator overrides or
tombstones a shipped rule by id, so a rename orphans every overlay entry naming
it. `donors_test.go` pins the id list, the kinds and every parameter for exactly
that reason — a rename must be a deliberate, visible edit here too.
