package adb

// record_row_first_test.go — R1: the row-first finalize invariant (the
// adb-session-stop row gate). The runner's stop is SIGTERM → grace → SIGKILL;
// the device finalize chain (SIGINT → size-stabilize → goadb pull) can exceed
// it, and a recorder SIGKILLed mid-pull must never lose the evidence row the
// stop gate polls. runSessionCapture must finalize the row (artifact-less)
// BEFORE the pull, then rewrite it with the artifact once the pull completes.

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adb "github.com/zach-klippenstein/goadb"
)

// blockedPullDevice is a sessionDevice whose GetFile pull blocks until released
// — the deterministic stand-in for the slow device round-trip racing the
// runner's stop ladder. The shell + STAT legs are inert so the bracket reaches
// the pull.
type blockedPullDevice struct {
	release   chan struct{}
	mp4Bytes  string
	finalSize int32
}

func (d *blockedPullDevice) RunCommand(cmd string, args ...string) (string, error) {
	return "mState=ON", nil // the display-ready probe sees the display up
}

func (d *blockedPullDevice) Stat(path string) (*adb.DirEntry, error) {
	return &adb.DirEntry{Size: d.finalSize}, nil
}

func (d *blockedPullDevice) OpenRead(path string) (io.ReadCloser, error) {
	<-d.release // hold the pull: the runner's SIGKILL analog lands HERE
	return io.NopCloser(strings.NewReader(d.mp4Bytes)), nil
}

// TestRunSessionCaptureFinalizesRowBeforePull asserts the row appears while the
// pull is still blocked (a stop raced by SIGKILL mid-pull keeps the row), and
// that the completed pull REWRITES the row with the mp4 artifact.
func TestRunSessionCaptureFinalizesRowBeforePull(t *testing.T) {
	const mp4 = "mp4-payload-for-the-row-first-gate"
	dev := &blockedPullDevice{release: make(chan struct{}), mp4Bytes: mp4, finalSize: int32(len(mp4))}
	cfg := RecorderConfig{
		StateDir:     t.TempDir(),
		SessionID:    "row-first-gate",
		Addr:         "127.0.0.1:1",
		StartBudget:  time.Second,
		FinalizeWait: 2 * time.Second,
	}
	done := make(chan struct{})
	type res struct {
		n   int64
		err error
	}
	rc := make(chan res, 1)
	go func() {
		n, err := runSessionCapture(dev, cfg, done)
		rc <- res{n, err}
	}()

	// Let the start bracket complete (the fake's Stat answers the capture-file
	// gate immediately), THEN close done as the SIGTERM analog — the real stop
	// races the device round-trips after an established session, never the start.
	time.Sleep(150 * time.Millisecond)
	close(done) // SIGTERM analog
	rowPath := filepath.Join(cfg.StateDir, evidenceFile)

	// The row must land while the pull is STILL blocked (it only unblocks when
	// the test releases it) — a stop raced by SIGKILL here would otherwise lose
	// the row the stop gate polls.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(rowPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: early evidence row missing while the pull is blocked — the stop gate would time out", rowPath)
		}
		time.Sleep(20 * time.Millisecond)
	}
	raw, err := os.ReadFile(rowPath)
	if err != nil {
		t.Fatalf("read early row: %v", err)
	}
	var early evidenceRow
	if err := json.Unmarshal(raw, &early); err != nil {
		t.Fatalf("decode early row: %v", err)
	}
	if len(early.Artifact) != 0 {
		t.Errorf("early row = %+v, want the artifact-less finalize (the pull has not completed)", early)
	}
	if early.Instrument != cfg.SessionID || early.Verb != "adb" {
		t.Errorf("early row instrument/verb = %s/%s, want %s/adb", early.Instrument, early.Verb, cfg.SessionID)
	}
	if _, err := os.Stat(filepath.Join(cfg.StateDir, finalMarker)); err != nil {
		t.Fatalf("FINAL marker missing with the early row: %v", err)
	}

	close(dev.release) // let the pull finish; SIGKILL would land here instead
	got := <-rc
	if got.err != nil {
		t.Fatalf("runSessionCapture: %v", got.err)
	}
	if got.n != int64(len(mp4)) {
		t.Errorf("pulled %d bytes, want %d", got.n, len(mp4))
	}
	finalRaw, err := os.ReadFile(rowPath)
	if err != nil {
		t.Fatalf("read final row: %v", err)
	}
	var final evidenceRow
	if err := json.Unmarshal(finalRaw, &final); err != nil {
		t.Fatalf("decode final row: %v", err)
	}
	if len(final.Artifact) != 1 || final.Artifact[0].Kind != "mp4" {
		t.Errorf("final row = %+v, want the mp4 artifact after the completed pull", final)
	}
	if final.Instrument != cfg.SessionID || final.Verb != "adb" {
		t.Errorf("final row instrument/verb = %s/%s, want %s/adb", final.Instrument, final.Verb, cfg.SessionID)
	}
}
