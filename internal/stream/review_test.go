package stream

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/agentculture/neurosymbolic-system/internal/senselog"
)

// --- finding 1: no encoding on the tick goroutine ---------------------------

// TestEnqueueDoesNotEncodeOnTheTickGoroutine is the structural half.
//
// enqueue is called from Write, Seam and onEvent — all three on the TICK
// goroutine, inside a 20 ms budget. CLAUDE.md's rule for an export leg is a
// timestamp and a bounded append, O(1), no encoding: json.Marshal over a pose
// allocates and its cost scales with the channel count, so it belongs on the
// writer goroutine, which is allowed to be slow. A future edit that moves it
// back fails here rather than showing up as an overrun on a robot.
func TestEnqueueDoesNotEncodeOnTheTickGoroutine(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "session.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing session.go: %v", err)
	}
	var found bool
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "enqueue" {
			continue
		}
		found = true
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			ident, ok := n.(*ast.Ident)
			if ok && ident.Name == "encodeFrame" {
				t.Errorf("enqueue calls encodeFrame at %s; encoding belongs in writeLoop",
					fset.Position(ident.Pos()))
			}
			return true
		})
	}
	if !found {
		t.Fatal("session.go declares no enqueue method; this guard is watching nothing")
	}
}

// TestAFrameThatWillNotEncodeIsANamedDropInTheWriter is the behavioural half.
//
// Deferring the encode moves the failure with it: a payload json cannot
// marshal is no longer refused on the tick goroutine, so the writer must name
// that drop itself — and must keep serving the frames behind it, since one bad
// payload is not a reason to stop a robot's telemetry.
func TestAFrameThatWillNotEncodeIsANamedDropInTheWriter(t *testing.T) {
	voc := toyVoc(t)
	logBuf := &syncBuffer{}
	reader, writer := io.Pipe()
	defer func() { _ = writer.Close() }()
	out := &syncBuffer{}

	srv, err := NewStdio(Config{Vocabulary: voc, HeartbeatEvery: -1},
		nil, newRecordingSense(), nil, senselog.New(logBuf), reader, out)
	if err != nil {
		t.Fatalf("NewStdio: %v", err)
	}
	sess := srv.session()
	sess.subscribed.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)
	defer srv.Close()

	// +Inf is a float64 json.Marshal refuses, so this pose cannot be encoded.
	sess.enqueue(KindPose, poseOut{
		V: Version, Kind: KindPose, Tick: 1,
		Channels: map[string][]float64{"ch_a": {math.Inf(1), 0}},
	})
	// A well-formed frame behind it must still reach the peer.
	if err := srv.Write(voc.Neutral()); err != nil {
		t.Fatalf("Write: %v", err)
	}

	deadline := time.After(2 * time.Second)
	for {
		if strings.Contains(string(logBuf.Bytes()), "reason=frame-too-large") {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("no encode-failure drop was logged:\n%s", logBuf.Bytes())
		case <-time.After(5 * time.Millisecond):
		}
	}
	deadline = time.After(2 * time.Second)
	for {
		if strings.Contains(string(out.Bytes()), "\"tick\":") {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the frame behind the unencodable one never reached the peer")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if srv.Stats().Drops == 0 {
		t.Error("the encode failure was not counted as a drop")
	}
}
