package adb

// session_test.go covers the adb: session method (plan Cutover E, E-4): the
// ON-DEVICE screenrecord bracket (launch contract, SIGINT stop, the goadb GetFile
// pull, finalize -> FINAL + row.json), the spawn request the provider submits to
// the runner's generic background-session service, and the runSession validation
// gates. The reverse-leg submission itself (InvokeProvider ClassVerb session) is
// exercised by the R10 bed — the venue-driving path.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	adb "github.com/zach-klippenstein/goadb"

	"github.com/opencharly/plugin-adb/candy/plugin-adb/params"
	"github.com/opencharly/spec/spec"
)

// fakeDevice is the sessionDevice fake: it records every RunCommand call (the
// screenrecord bracket contract), serves device-file Stats from a size script,
// and streams a canned MP4 over OpenRead (the GetFile pull). Stat sizes are
// consumed in order — the stop path's finalize-wait sees growing sizes then a
// stable one.
type fakeDevice struct {
	mu    sync.Mutex
	calls []string // "cmd arg1 arg2" — the invocation log

	remoteSizes []int32 // scripted Stat sizes for the session remote path (-1 = not yet created)
	sizeIdx     int
	remoteBytes string // the canned "device file" OpenRead streams
}

func (f *fakeDevice) RunCommand(cmd string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, strings.Join(append([]string{cmd}, args...), " "))
	return "", nil
}

func (f *fakeDevice) Stat(path string) (*adb.DirEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sizeIdx >= len(f.remoteSizes) {
		return nil, os.ErrNotExist
	}
	size := f.remoteSizes[f.sizeIdx]
	f.sizeIdx++
	if size < 0 {
		return nil, os.ErrNotExist
	}
	return &adb.DirEntry{Name: filepath.Base(path), Size: size}, nil
}

func (f *fakeDevice) OpenRead(path string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader([]byte(f.remoteBytes))), nil
}

func (f *fakeDevice) callsJoined() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.calls, " | ")
}

// TestStartScreenrecordInvocation pins the EXACT device launch contract: one
// "sh -c" line that backgrounds screenrecord with the safety time-limit and the
// session's on-device path, with all fds redirected so the adb shell returns.
func TestStartScreenrecordInvocation(t *testing.T) {
	fake := &fakeDevice{remoteSizes: []int32{0, 128, 128}}
	cfg := RecorderConfig{SessionID: "bed.member.cap", StateDir: "/x"}
	if err := startScreenrecord(fake, cfg); err != nil {
		t.Fatalf("startScreenrecord: %v", err)
	}
	want := "sh -c nohup screenrecord --time-limit 1800 /sdcard/bed.member.cap.mp4 >/dev/null 2>&1 &"
	if got := fake.callsJoined(); got != want {
		t.Errorf("invocation = %q, want %q", got, want)
	}
	// a nonzero size on the FIRST sample: the launch already succeeded.
	fake2 := &fakeDevice{remoteSizes: []int32{1}}
	if err := startScreenrecord(fake2, RecorderConfig{SessionID: "s"}); err != nil {
		t.Fatalf("startScreenrecord (first sample present): %v", err)
	}
}

// TestStartScreenrecordTimesOutWithoutTheFile guards the start gate's honest
// failure: a launch that never produces the capture file fails the START (before
// a whole phase burns on a dead recorder).
func TestStartScreenrecordTimesOutWithoutTheFile(t *testing.T) {
	fake := &fakeDevice{remoteSizes: nil} // Stat never finds the file
	if err := startScreenrecord(fake, RecorderConfig{SessionID: "s", StartBudget: 120 * time.Millisecond}); err == nil ||
		!strings.Contains(err.Error(), "did not appear") {
		t.Fatalf("startScreenrecord with no capture file: want timeout error, got %v", err)
	}
}

// TestStopScreenrecordSigsAndWaitsFinalize covers the stop contract: pkill -INT
// screenrecord, then the Stat finalize-wait returns only once the size is nonzero
// AND stable across two samples.
func TestStopScreenrecordSigsAndWaitsFinalize(t *testing.T) {
	t.Run("stability", func(t *testing.T) {
		// growing sizes (recording streams chunks), then the final stable size.
		fake := &fakeDevice{remoteSizes: []int32{100, 110, 120, 120, 120}}
		size, err := stopScreenrecord(fake, RecorderConfig{SessionID: "s"})
		if err != nil {
			t.Fatalf("stopScreenrecord: %v", err)
		}
		if size != 120 {
			t.Errorf("finalized size = %d, want 120", size)
		}
		if got := fake.callsJoined(); got != "sh -c pkill -INT screenrecord" {
			t.Errorf("stop invocation = %q, want the SIGINT pkill", got)
		}
	})
	t.Run("started-last-sample", func(t *testing.T) {
		// a nonzero size on the FIRST sample is allowed (the recording may be
		// finalized before the first poll).
		fake := &fakeDevice{remoteSizes: []int32{50, 50}}
		size, err := stopScreenrecord(fake, RecorderConfig{SessionID: "s"})
		if err != nil || size != 50 {
			t.Fatalf("stopScreenrecord = (%d, %v), want (50, nil)", size, err)
		}
	})
}

// TestStopScreenrecordMissingFileFailsHonestly: SIGINT with no capture file is a
// bounded failure, not a hang.
func TestStopScreenrecordMissingFileFailsHonestly(t *testing.T) {
	fake := &fakeDevice{remoteSizes: nil}
	if _, err := stopScreenrecord(fake, RecorderConfig{SessionID: "s", FinalizeWait: 120 * time.Millisecond}); err == nil ||
		!strings.Contains(err.Error(), "missing after") {
		t.Fatalf("stopScreenrecord with no file: want time-bounded error, got %v", err)
	}
}

// TestGetFilePullsOverTheSyncReader is the GetFile path contract: the device file
// (OpenRead — the goadb sync-protocol read that "adb pull" uses, the existing
// device-file GetFile path) lands byte-identical at the host destination.
func TestGetFilePullsOverTheSyncReader(t *testing.T) {
	fake := &fakeDevice{remoteBytes: "\x00\x00\x00\x18ftypmp42..."}
	dst := filepath.Join(t.TempDir(), "out.mp4")
	n, err := getFile(fake, "/sdcard/s.mp4", dst)
	if err != nil {
		t.Fatalf("getFile: %v", err)
	}
	if n != int64(len(fake.remoteBytes)) {
		t.Errorf("pulled %d bytes, want %d", n, len(fake.remoteBytes))
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read pulled file: %v", err)
	}
	if string(got) != fake.remoteBytes {
		t.Errorf("pulled content = %q, want the synced device bytes", got)
	}
}

// TestRunSessionCaptureEndToEnd drives the whole bracket with the fake: start,
// done closes (the SIGTERM analog), stop + pull + finalize. The state dir then
// carries the pulled <session>.mp4, the FINAL marker, and the evidence row.json
// with the mp4 artifact (the shared #EvidenceRow shape).
func TestRunSessionCaptureEndToEnd(t *testing.T) {
	stateDir := t.TempDir()
	fake := &fakeDevice{
		remoteSizes: []int32{0, 100, 100}, // start wait sees it appear; stop sees it stable
		remoteBytes: "\x00\x00\x00\x18ftypmp42...",
	}
	cfg := RecorderConfig{
		StateDir:  stateDir,
		SessionID: "bed.member.cap",
		Venue:     "check-android-emulator-pod",
		Phase:     "live",
	}
	done := make(chan struct{})
	type res struct {
		n   int64
		err error
	}
	rc := make(chan res, 1)
	go func() {
		n, err := runSessionCapture(fake, cfg, done)
		rc <- res{n, err}
	}()
	time.Sleep(30 * time.Millisecond)
	close(done)
	got := <-rc
	if got.err != nil {
		t.Fatalf("runSessionCapture: %v", got.err)
	}
	if got.n != int64(len(fake.remoteBytes)) {
		t.Errorf("bytes = %d, want %d", got.n, len(fake.remoteBytes))
	}
	// the pulled MP4 landed in the state dir under the session name.
	mp4 := filepath.Join(stateDir, "bed.member.cap.mp4")
	b, err := os.ReadFile(mp4)
	if err != nil {
		t.Fatalf("session mp4 missing after stop: %v", err)
	}
	if string(b) != fake.remoteBytes {
		t.Errorf("mp4 content mismatch")
	}
	// the FINAL marker.
	if _, err := os.Stat(filepath.Join(stateDir, finalMarker)); err != nil {
		t.Fatalf("FINAL marker missing after stop: %v", err)
	}
	// the evidence row.
	raw, err := os.ReadFile(filepath.Join(stateDir, evidenceFile))
	if err != nil {
		t.Fatalf("row.json missing after stop: %v", err)
	}
	var row evidenceRow
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatalf("decode row.json: %v", err)
	}
	tw := evidenceRow{
		Instrument: "bed.member.cap",
		Origin:     "session",
		Verb:       "adb",
		Venue:      "check-android-emulator-pod",
		Phase:      "live",
		Artifact:   []evidenceArtifact{{Path: mp4, Kind: "mp4"}},
	}
	if row.Instrument != tw.Instrument || row.Origin != tw.Origin || row.Verb != tw.Verb ||
		row.Venue != tw.Venue || row.Phase != tw.Phase || len(row.Artifact) != 1 ||
		row.Artifact[0].Path != tw.Artifact[0].Path || row.Artifact[0].Kind != tw.Artifact[0].Kind {
		t.Errorf("row.json = %+v, want %+v", row, tw)
	}
	// the bracket contract: the start launch + the SIGINT stop both ran.
	if got := fake.callsJoined(); !strings.Contains(got, "sh -c nohup screenrecord") ||
		!strings.Contains(got, "sh -c pkill -INT screenrecord") {
		t.Errorf("bracket invocations = %q, want start + SIGINT stop", got)
	}
}

// TestRunSessionCaptureStartFailureFinalizesEmptyRow: a session whose start never
// produces the capture file still finalizes a row (artifact-less) so the stop
// finds a record — the failed capture is visible, never a phantom.
func TestRunSessionCaptureStartFailureFinalizesEmptyRow(t *testing.T) {
	stateDir := t.TempDir()
	fake := &fakeDevice{remoteSizes: nil} // Stat never finds the file
	done := make(chan struct{})
	_, err := runSessionCapture(fake, RecorderConfig{StateDir: stateDir, SessionID: "s", StartBudget: 120 * time.Millisecond}, done)
	if err == nil {
		t.Fatalf("runSessionCapture with a dead start: want error")
	}
	if _, serr := os.Stat(filepath.Join(stateDir, evidenceFile)); serr != nil {
		t.Fatalf("row.json missing after start failure: %v", serr)
	}
	raw, _ := os.ReadFile(filepath.Join(stateDir, evidenceFile))
	var row evidenceRow
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatalf("decode row.json: %v", err)
	}
	if len(row.Artifact) != 0 {
		t.Errorf("failed session row must carry no artifact, got %+v", row.Artifact)
	}
}

// TestBuildSessionSpawn asserts the exact spawn request the provider submits to
// the runner's generic session service: this plugin's binary in recorder mode +
// the device endpoint/session identity env, with the venue default from the
// CheckEnv snapshot applied.
func TestBuildSessionSpawn(t *testing.T) {
	in := &params.AdbInput{
		SessionId: "bed.member.cap",
		StateDir:  "/var/run/checks/bed/x",
		Phase:     "live",
	}
	req := buildSessionSpawn(in, "127.0.0.1:35002", "emulator-5554", "/usr/lib/charly/plugin-adb", "check-android-emulator-pod", "")
	if req.Op != "spawn" {
		t.Errorf("op = %q, want spawn", req.Op)
	}
	if req.SessionID != "bed.member.cap" {
		t.Errorf("session_id = %q", req.SessionID)
	}
	if len(req.Command) != 2 || req.Command[0] != "/usr/lib/charly/plugin-adb" || req.Command[1] != "__dummy-arg" {
		t.Errorf("command = %v, want [<self> __dummy-arg]", req.Command)
	}
	if req.Env[EnvRecorder] != "1" {
		t.Errorf("CHARLY_ADB_RECORDER = %q, want 1", req.Env[EnvRecorder])
	}
	if req.Env[EnvAddr] != "127.0.0.1:35002" {
		t.Errorf("CHARLY_ADB_ADDR = %q, want the provider-resolved addr", req.Env[EnvAddr])
	}
	if req.Env[EnvSerial] != "emulator-5554" {
		t.Errorf("CHARLY_ADB_SERIAL = %q", req.Env[EnvSerial])
	}
	if req.Env[EnvStateDir] != "/var/run/checks/bed/x" {
		t.Errorf("CHARLY_ADB_STATE_DIR = %q", req.Env[EnvStateDir])
	}
	if req.Env[EnvSessionID] != "bed.member.cap" {
		t.Errorf("CHARLY_ADB_SESSION_ID = %q", req.Env[EnvSessionID])
	}
	if req.Env[EnvVenue] != "check-android-emulator-pod" {
		t.Errorf("CHARLY_ADB_VENUE = %q, want check-android-emulator-pod (CheckEnv default)", req.Env[EnvVenue])
	}
	if req.Env[EnvPhase] != "live" {
		t.Errorf("CHARLY_ADB_PHASE = %q, want live", req.Env[EnvPhase])
	}
	// serial defaulting: an empty serial in the env still spawns the emulator default.
	def := buildSessionSpawn(&params.AdbInput{SessionId: "s", StateDir: "/x"}, "127.0.0.1:5037", "", "/e", "", "")
	if def.Env[EnvSerial] != "emulator-5554" {
		t.Errorf("default serial = %q, want emulator-5554", def.Env[EnvSerial])
	}
}

// TestRunSessionValidation guards the required-modifier semantics of the session
// method WITHOUT the real reverse leg: the action gate fails before any
// submission, and a well-formed session reaches the submission dispatch (the
// stub cc answers an error, proving the InvokeProvider path is exercised — the
// session_id/state_dir have provider-side fallbacks exactly like vnc's, so no
// runSession-level gate rejects a plan-step input).
func TestRunSessionValidation(t *testing.T) {
	ctx := context.Background()
	// a plan-step env shape: an explicit AdbAddr (deploy/status style) so
	// sessionStart resolves without container inspection.
	env := &adbEnv{AdbAddr: "127.0.0.1:5037"}

	// bogus action -> error before any submission.
	in := &params.AdbInput{Action: "bogus"}
	if _, err := runSession(ctx, stubCC{}, env, in, ""); err == nil ||
		!strings.Contains(err.Error(), "requires action") {
		t.Fatalf("bogus action: want error, got %v", err)
	}
	// a well-formed start reaches the submission (the stub cc answers an error —
	// proving the dispatch path is exercised, not skipped).
	in2 := &params.AdbInput{Action: "start", SessionId: "s"}
	if _, err := runSession(ctx, stubCC{}, env, in2, ""); err == nil ||
		!strings.Contains(err.Error(), "stub: submission reached") {
		t.Fatalf("start with stub cc: want the stub's error, got %v", err)
	}
	// a well-formed stop reaches the submission too.
	in3 := &params.AdbInput{Action: "stop", SessionId: "s"}
	if _, err := runSession(ctx, stubCC{}, env, in3, ""); err == nil ||
		!strings.Contains(err.Error(), "stub: submission reached") {
		t.Fatalf("stop with stub cc: want the stub's error, got %v", err)
	}
}

// stubCC embeds spec.CheckContext (never invoked elsewhere) and answers the
// reverse-leg submission with a sentinel error — the validation-test stand-in,
// proving a well-formed session dispatches over InvokeProvider instead of being
// short-circuited by a gate.
type stubCC struct{ spec.CheckContext }

func (stubCC) InvokeProvider(ctx context.Context, class, word, op string, paramsJSON, env []byte) ([]byte, error) {
	return nil, fmt.Errorf("stub: submission reached the reverse-leg dispatch (class %s word %s op %s)", class, word, op)
}

// TestWaitForEvidenceRow covers the stop path's bounded row wait: the row appears
// later (the recorder's device chain lags the runner's stop return), and a row
// that never lands fails the stop with the path.
func TestWaitForEvidenceRow(t *testing.T) {
	dir := t.TempDir()
	rowPath := filepath.Join(dir, evidenceFile)

	// the row lands late, but within the deadline.
	go func() {
		time.Sleep(150 * time.Millisecond)
		_ = os.WriteFile(rowPath, []byte("{\"instrument\":\"s\"}"), 0o644)
	}()
	raw, err := waitForEvidenceRow(context.Background(), rowPath, 2*time.Second)
	if err != nil {
		t.Fatalf("waitForEvidenceRow (late row): %v", err)
	}
	if !strings.Contains(string(raw), "s") {
		t.Errorf("row content = %q", raw)
	}

	// a row that never lands fails at the deadline, naming the path.
	missing := filepath.Join(dir, "missing", evidenceFile)
	start := time.Now()
	if _, err := waitForEvidenceRow(context.Background(), missing, 200*time.Millisecond); err == nil ||
		!strings.Contains(err.Error(), missing) {
		t.Fatalf("waitForEvidenceRow (never lands): want deadline error naming the path, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("deadline not bounded")
	}
}

// TestFinalizeSessionWritesMarkerAndRow guards finalize's exact artifacts: the
// FINAL marker carries the pulled byte count, the row carries the mp4 artifact.
func TestFinalizeSessionWritesMarkerAndRow(t *testing.T) {
	stateDir := t.TempDir()
	cfg := RecorderConfig{StateDir: stateDir, SessionID: "bed.member.cap", Venue: "v", Phase: "live"}
	mp4 := filepath.Join(stateDir, "bed.member.cap.mp4")
	if err := finalizeSession(cfg, mp4, 4096); err != nil {
		t.Fatalf("finalizeSession: %v", err)
	}
	marker, err := os.ReadFile(filepath.Join(stateDir, finalMarker))
	if err != nil {
		t.Fatalf("FINAL marker: %v", err)
	}
	if want := "final bytes=4096\n"; string(marker) != want {
		t.Errorf("FINAL content = %q, want %q", marker, want)
	}
}
