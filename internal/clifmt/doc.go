// Package clifmt is the neurosymbolic-engine's output/error/exit contract: the
// same one the Python CLI in neurosymbolic_system/cli/_errors.py and
// _output.py already enforces, ported so a Go verb and a Python verb read
// identically to whatever calls either.
//
//   - results go to stdout, errors and diagnostics go to stderr, and the two
//     streams are never mixed;
//   - a failure is a structured CliError{Code, Message, Remediation}: text
//     mode renders "error: <message>" then, when Remediation is non-empty,
//     "hint: <remediation>"; JSON mode renders {"code","message","remediation"}
//     as single-line JSON;
//   - the exit-code policy is 0 success, 1 user-input error, 2
//     environment/setup error, 3+ reserved;
//   - --json must work even for a parse-time failure (a bad verb, a bad flag,
//     before any verb-specific parsing runs), so a caller pre-scans argv with
//     HasJSONFlag/StripJSONFlag rather than discovering --json only after a
//     flag.FlagSet has already succeeded;
//   - no panic and no un-annotated error may ever reach a caller as a Go stack
//     trace — Guard recovers and normalizes anything unexpected into a
//     CliError in the environment-error bucket.
//
// This shape is deliberately the same one culture-nodes' internal/clifmt
// implements (see ../../culture-nodes/internal/clifmt in the sibling
// checkout) — cited for its shape, not its code, per this repo's
// read-siblings-never-copy convention. internal/mgmt is the one caller: every
// verb answers through a mgmt.Handler, which renders its result or error
// through this package exactly once, so cmd/neurosymbolic-engine/main.go and
// the future socket front both get the same two-stream, two-mode contract for
// free.
package clifmt
