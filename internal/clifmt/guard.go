package clifmt

import "errors"

// Guard runs fn and recovers any panic, converting it into a CliError in the
// ExitEnv bucket so no Go stack trace ever reaches a caller. A non-nil error
// fn returns is normalized the same way, UNLESS it already is (or wraps) a
// *CliError, in which case that CliError passes through with its own Code
// intact — a deliberately raised user error (ExitUser) must never be
// reclassified as an environment error just because Guard also happens to
// catch panics.
//
// Guard returns nil on success. internal/mgmt's Handler wraps every verb body
// in Guard, which is what satisfies "wrap every panic in Handle into a
// CliError{Code: 2}".
func Guard(fn func() error) (result *CliError) {
	defer func() {
		if r := recover(); r != nil {
			result = newPanicError(r)
		}
	}()

	err := fn()
	if err == nil {
		return nil
	}

	var cliErr *CliError
	if errors.As(err, &cliErr) {
		return cliErr
	}
	return NewEnvError(err.Error(), "")
}
