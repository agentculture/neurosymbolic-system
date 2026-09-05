package mgmt

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// errDifferentRuleSet is the sentinel a verify function returns when the
// candidate file loads but does not carry the rule set it was derived from.
var errDifferentRuleSet = errors.New("the migrated file does not load as the same rule set")

// verificationError wraps whatever a replaceFileAtomically verify function
// refused with, so the caller can tell "your content did not pass its own
// check" apart from "the filesystem would not cooperate" — two failures with
// very different remediations.
type verificationError struct {
	cause error
}

func (e *verificationError) Error() string { return e.cause.Error() }
func (e *verificationError) Unwrap() error { return e.cause }

// replaceFileAtomically writes content to a temporary file in dest's own
// directory, hands that temp path to verify, and only on success renames it
// over dest.
//
// dest is never truncated, opened for writing, or removed. That matters
// because dest is frequently a file the operator already has — `rules migrate
// --force` overwrites a previous migration — and the two failure modes of the
// naive "write then check then delete" shape both cost them that file: a
// short write leaves a truncated destination, and a failed validation deletes
// a destination this process did not create. A rename within one directory is
// atomic on every filesystem this engine targets, so a reader of dest sees
// either the old file or the new one and never a half-written one.
//
// The temp file is created in dest's directory, not in $TMPDIR, precisely so
// the rename stays within one filesystem; a cross-device rename would fail
// and hand back the very failure this function exists to avoid.
func replaceFileAtomically(dest string, content []byte, verify func(tmpPath string) error) error {
	dir := filepath.Dir(dest)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(dest)+".tmp-*")
	if err != nil {
		return fmt.Errorf("could not create a temporary file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	// Removed on every failure path. After a successful rename the path no
	// longer exists, and the remove is a harmless no-op.
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("could not write %s: %w", tmpPath, err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("could not set the mode of %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("could not close %s: %w", tmpPath, err)
	}

	if verify != nil {
		if err := verify(tmpPath); err != nil {
			return &verificationError{cause: err}
		}
	}

	if err := os.Rename(tmpPath, dest); err != nil {
		return fmt.Errorf("could not replace %s: %w", dest, err)
	}
	return nil
}
