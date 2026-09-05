package adaptor

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// maxTrailingQuoted is how much of the trailing content a refusal quotes back.
// Enough to recognize what was left behind, short enough that a whole second
// document does not become the error message.
const maxTrailingQuoted = 60

// decodedToEOF reports whether dec has consumed its whole input, ignoring
// trailing whitespace. It returns a non-nil error describing what came after
// the document otherwise.
//
// encoding/json's Decoder is a STREAM decoder: one Decode reads one value and
// stops. Everything after it — a second document, a stray array, a paragraph
// of notes — is silently ignored. For a config file that is the worst failure
// shape there is: the operator's edit is in the file, the file loads, and
// nothing they wrote takes effect. Refusing costs one call and turns it into
// a message naming the file.
func decodedToEOF(dec *json.Decoder) error {
	var extra json.RawMessage
	err := dec.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("found %s", quoteTrailing(extra))
}

func quoteTrailing(raw []byte) string {
	text := strings.TrimSpace(string(raw))
	if len(text) > maxTrailingQuoted {
		text = text[:maxTrailingQuoted] + "..."
	}
	return fmt.Sprintf("%q", text)
}
