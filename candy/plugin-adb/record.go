package adb

// record.go — the DETACHED ON-DEVICE session recorder (plan Cutover E, E-4).
// `adb: session start` hands THIS binary (in recorder mode, env
// CHARLY_ADB_RECORDER=1) to the runner's generic background-session service
// (plugin-check's compiled-in verb:session seam) — the plugin-vnc session
// pattern (Cutover E, E-1) applied to the adb transport. Unlike the vnc/spice
// host-side recorders (the host holds the wire), the CAPTURE runs ON THE DEVICE:
// the recorder's job is the bracket. On start it launches the device-side
// `screenrecord` detached (`nohup screenrecord --time-limit 1800
// /sdcard/<session_id>.mp4 >/dev/null 2>&1 &` — the standard adb background
// idiom: the adb shell exits the moment the redirected fds close), verifies the
// capture file appeared, then holds the session. On SIGTERM/SIGINT (the phase
// end) it stops the device recorder with SIGINT (`pkill -INT screenrecord` —
// screenrecord's own signal handler finalizes the MP4), waits for the size to
// stabilize (the moov atom lands at finalize), and PULLS the MP4 via the goadb
// device-file GetFile path (OpenRead — the sync wire `adb pull` uses, the
// reciprocal of the committed-APK OpenWrite push in install.go) into
// <state_dir>/<session_id>.mp4, then finalizes: the deterministic FINAL marker +
// the evidence row.json ("instrument"/"origin"/"verb"/"venue"/"phase"/"artifact"
// — the SHARED #EvidenceRow shape, plan §4 A-task-1). While it runs, the
// PROVIDER spawns no process and knows no transport.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	adb "github.com/zach-klippenstein/goadb"
)

// Recorder-mode env contract between the provider (buildSessionSpawn, the spawn
// env) and cmd/serve's recorder mode (the reader). The provider builds these keys;
// the recorder binary reads them detached.
const (
	EnvRecorder  = "CHARLY_ADB_RECORDER"
	EnvAddr      = "CHARLY_ADB_ADDR"
	EnvSerial    = "CHARLY_ADB_SERIAL"
	EnvStateDir  = "CHARLY_ADB_STATE_DIR"
	EnvSessionID = "CHARLY_ADB_SESSION_ID"
	EnvVenue     = "CHARLY_ADB_VENUE"
	EnvPhase     = "CHARLY_ADB_PHASE"
)

// Artifact + marker constants inside a session state dir.
const (
	evidenceFile = "row.json"
	finalMarker  = "FINAL"
)

// screenrecord wire constants. screenrecordMaxSeconds is the tool's OWN max 30min
// time-limit cap: the stop is phase-driven (the bracket ends it), the cap only
// guards a forgotten stop (screenrecord's default limit is 180s — too short for
// a real phase).
const (
	screenrecordMaxSeconds        = 1800
	screenrecordStartBudget       = 5 * time.Second        // wait for the capture file to appear after launch
	screenrecordFinalizeWait      = 10 * time.Second       // wait for the size to stabilize after SIGINT
	screenrecordStabilityGap      = 750 * time.Millisecond // two equal Stat samples = finalized
	screenrecordRemoteDir         = "/sdcard"
	screenrecordDeviceReadyBudget = 90 * time.Second // max wait for the emulator to attach to adb at phase start
)

// evidenceRow mirrors the shared #EvidenceRow shape (plan §4 A-task-1) — the
// minimal session subset the recorder writes and sessionStop reads back (the
// CLOSED envelope: instrument/origin/verb/venue/phase/artifact). No
// plugin-specific manifest code: the runner's evidence phase consumes the
// general shape.
type evidenceRow struct {
	Instrument string             `json:"instrument"`
	Origin     string             `json:"origin"`
	Verb       string             `json:"verb"`
	Venue      string             `json:"venue,omitempty"`
	Phase      string             `json:"phase,omitempty"`
	Artifact   []evidenceArtifact `json:"artifact,omitempty"`
}

type evidenceArtifact struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

// sessionDevice is the subset of *adb.Device the recorder drives (RunCommand for
// the bracket, Stat for finalize detection, OpenRead for the GetFile pull) — an
// interface so the whole recorder is unit-testable with a fake when no device is
// available (B12, mirroring the vnc frameSource fake).
type sessionDevice interface {
	RunCommand(cmd string, args ...string) (string, error)
	Stat(path string) (*adb.DirEntry, error)
	OpenRead(path string) (io.ReadCloser, error)
}

// RecorderConfig is the detached-session recorder's full runtime contract (the
// spawn env fields, decoded).
type RecorderConfig struct {
	Addr      string // the device's adb-server "host:port" (host-reachable; the verb's resolved addr)
	Serial    string // device serial (default emulator-5554)
	StateDir  string // the run's state dir: <session>.mp4 + FINAL + row.json land here
	SessionID string // the venue-scoped session id — stamped into the evidence row
	Venue     string // evidence-row provenance
	Phase     string // evidence-row provenance (build|live|update|teardown)

	// Bounded-wait budgets (test-overridable; zero = the package constants).
	StartBudget       time.Duration // wait for the capture file to appear after launch
	FinalizeWait      time.Duration // wait for the size to stabilize after SIGINT
	DeviceReadyBudget time.Duration // max wait for the device to attach to adb before the launch retries give up
}

// deviceReadyBudget resolves the device-online pre-flight wait (default
// screenrecordDeviceReadyBudget — an emulator pod's live phase can start while
// the Android system is still booting).
func (cfg RecorderConfig) deviceReadyBudget() time.Duration {
	if cfg.DeviceReadyBudget > 0 {
		return cfg.DeviceReadyBudget
	}
	return screenrecordDeviceReadyBudget
}

// startBudget resolves the start gate's wait (default screenrecordStartBudget).
func (cfg RecorderConfig) startBudget() time.Duration {
	if cfg.StartBudget > 0 {
		return cfg.StartBudget
	}
	return screenrecordStartBudget
}

// finalizeWait resolves the stop gate's wait (default screenrecordFinalizeWait).
func (cfg RecorderConfig) finalizeWait() time.Duration {
	if cfg.FinalizeWait > 0 {
		return cfg.FinalizeWait
	}
	return screenrecordFinalizeWait
}

// remotePath is the on-device capture path for a session.
func (cfg RecorderConfig) remotePath() string {
	return filepath.Join(screenrecordRemoteDir, cfg.SessionID+".mp4")
}

// artifactPath is the host-side pull target inside the state dir.
func (cfg RecorderConfig) artifactPath() string {
	return filepath.Join(cfg.StateDir, cfg.SessionID+".mp4")
}

// screenrecordRetryInterval paces the device-ready launch retries: an emulator
// pod's live phase can begin while the Android system is still booting (the
// device not yet attached to the adb server, and its display/media not yet
// ready even once the shell answers), so the first launch attempt legitimately
// fails — the retry loop rides out the boot instead of failing the whole
// session on a transient DeviceNotFound or a screenrecord that cannot produce
// yet.
const deviceReadyRetryInterval = time.Second

// startScreenrecord launches the device-side recorder detached and waits for the
// capture file to appear (a launch that fails — no screenrecord, a missing
// display — fails the START, before the phase burns a whole bracket on a
// recorder that never captured). The launch is retried within a bounded
// DEVICE-READY budget (RecorderConfig.DeviceReadyBudget, default
// screenrecordDeviceReadyBudget): a fresh emulator pod accepts the adb shell
// while its display/media still boots, so the file gate gets its own short
// per-shot budget and a shot that produced nothing is retried, not failed
// hard. done closes at phase stop, so an abort mid-wait is a clean early
// finalize, never a hung recorder.
func startScreenrecord(dev sessionDevice, cfg RecorderConfig, done <-chan struct{}) error {
	remote := cfg.remotePath()
	deadline := time.Now().Add(cfg.deviceReadyBudget())
	for {
		select {
		case <-done:
			return fmt.Errorf("screenrecord start: device not ready within the phase (session aborted)")
		default:
		}
		// The standard adb background idiom: nohup + fds redirected, so the adb
		// shell returns the moment the launch line is accepted. --time-limit is the
		// safety cap, NOT the stop (the phase-driven SIGINT bracket ends it).
		cmd := fmt.Sprintf("nohup screenrecord --time-limit %d %s >/dev/null 2>&1 &", screenrecordMaxSeconds, remote)
		launched := false
		if _, err := dev.RunCommand("sh", "-c", cmd); err == nil {
			launched = true
			// Launch accepted: the capture file must appear within the per-shot
			// start budget, else the device accepted the shell but screenrecord
			// cannot produce yet (booting display/media) — that shot is retried.
			shot := time.Now().Add(cfg.startBudget())
			for time.Now().Before(shot) {
				if _, err := dev.Stat(remote); err == nil {
					return nil
				}
				time.Sleep(250 * time.Millisecond)
			}
		}
		// Either the device wasn't attached (launch failed on the adb wire) or the
		// launch was accepted but no capture file appeared. Both ride out within
		// the device-ready budget; the budget-exhausted error names the honest
		// cause (a never-accepted launch vs. a launch that never produced).
		if time.Now().After(deadline) {
			if launched {
				return fmt.Errorf("screenrecord start: %s did not appear within %s (screenrecord launch failed on device?)", remote, cfg.deviceReadyBudget())
			}
			return fmt.Errorf("screenrecord start: device never became ready inside %s (still booting?); last launch failed on the adb wire", cfg.deviceReadyBudget())
		}
		time.Sleep(deviceReadyRetryInterval)
	}
}

// stopScreenrecord SIGINTs the device recorder and waits for the MP4 to finalize:
// the file's size must be nonzero AND stable across two samples (screenrecord
// streams the interleaved chunks while recording and lands the moov atom at
// finalize — a stable size is the "recording done" signal; a pkill that already
// finds the process gone is tolerated, the size gate is the real assertion).
// Returns the finalized size (goadb's DirEntry.Size is int32).
func stopScreenrecord(dev sessionDevice, cfg RecorderConfig) (int64, error) {
	remote := cfg.remotePath()
	// Best-effort SIGINT (the pkill pattern match on the binary name; a gone
	// screenrecord is not an error — the size gate below makes the call).
	_, _ = dev.RunCommand("sh", "-c", "pkill -INT screenrecord")
	deadline := time.Now().Add(cfg.finalizeWait())
	last, have := int64(-1), false
	for time.Now().Before(deadline) {
		st, err := dev.Stat(remote)
		if err != nil {
			time.Sleep(250 * time.Millisecond)
			continue
		}
		size := int64(st.Size)
		if size <= 0 {
			last, have = size, true
			time.Sleep(screenrecordStabilityGap)
			continue
		}
		if have && last == size {
			return size, nil
		}
		last, have = size, true
		time.Sleep(screenrecordStabilityGap)
	}
	if have {
		return last, fmt.Errorf("screenrecord stop: %s not finalized within %s (last size %d; is screenrecord still streaming?)", remote, cfg.finalizeWait(), last)
	}
	return 0, fmt.Errorf("screenrecord stop: %s missing after %s (recording never started?)", remote, cfg.finalizeWait())
}

// getFile pulls one device file to the host via the goadb sync-protocol read
// (Device.OpenRead — the `adb pull` wire, the EXISTING device-file GetFile path;
// install.go's committed-APK push is its OpenWrite reciprocal). Streams straight
// to dst; returns the byte count.
func getFile(dev sessionDevice, remote, dst string) (int64, error) {
	rc, err := dev.OpenRead(remote)
	if err != nil {
		return 0, fmt.Errorf("adb get %s: %w", remote, err)
	}
	defer rc.Close() //nolint:errcheck
	f, err := os.Create(dst)
	if err != nil {
		return 0, fmt.Errorf("adb get: create %s: %w", dst, err)
	}
	n, cpErr := io.Copy(f, rc)
	if cerr := f.Close(); cpErr == nil {
		cpErr = cerr
	}
	if cpErr != nil {
		return n, fmt.Errorf("adb get %s → %s: %w", remote, dst, cpErr)
	}
	return n, nil
}

// finalizeSession writes the deterministic end-of-stream marker + the evidence
// row into the state dir. Called ONCE, on the stop path — a session is complete
// only when row.json is on disk (the recorder finalizes the row even when the
// pull failed: the evidence envelope stays closed-shaped and the missing
// artifact is the visible failure).
func finalizeSession(cfg RecorderConfig, mp4 string, size int64) error {
	row := evidenceRow{
		Instrument: cfg.SessionID,
		Origin:     "session",
		Verb:       "adb",
		Venue:      cfg.Venue,
		Phase:      cfg.Phase,
	}
	if size > 0 {
		row.Artifact = []evidenceArtifact{{Path: mp4, Kind: "mp4"}}
	}
	b, err := json.MarshalIndent(row, "", "  ")
	if err != nil {
		return fmt.Errorf("recorder: marshal evidence row: %w", err)
	}
	b = append(b, '\n')
	if err := os.WriteFile(filepath.Join(cfg.StateDir, evidenceFile), b, 0o644); err != nil {
		return fmt.Errorf("recorder: write %s: %w", evidenceFile, err)
	}
	marker := fmt.Sprintf("final bytes=%d\n", size)
	return os.WriteFile(filepath.Join(cfg.StateDir, finalMarker), []byte(marker), 0o644)
}

// RunSessionRecorder is the detached-mode engine (cmd/serve, recorder mode):
// dial the device (via the goadb wire), then run the whole bracket
// (runSessionCapture). Returns the pulled byte count. A dial failure still
// finalizes an artifact-less row so a stop never sees a phantom "never
// finalized" (the dial error rides recorder.log).
func RunSessionRecorder(cfg RecorderConfig, done <-chan struct{}) (int64, error) {
	if cfg.StateDir == "" || cfg.SessionID == "" {
		return 0, fmt.Errorf("recorder: empty state dir or session id")
	}
	if cfg.Addr == "" {
		return 0, fmt.Errorf("recorder: empty adb addr")
	}
	if err := os.MkdirAll(cfg.StateDir, 0o755); err != nil {
		return 0, fmt.Errorf("recorder: create state dir: %w", err)
	}
	// Dial the device signal-aware: the runner's stop (SIGTERM → done) can arrive
	// WHILE the dial is still blocking on the adb wire (a fresh emulator pod's
	// published adb port may not answer for seconds). A dial raced by done must
	// finalize the artifact-less evidence row instead of dying to the runner's
	// TERM→KILL grace — the adb-session-stop gate polls that row.
	type dialResult struct {
		dev sessionDevice
		err error
	}
	dialCh := make(chan dialResult, 1)
	go func() {
		dev, err := adbDeviceForAddr(cfg.Addr, cfg.Serial)
		dialCh <- dialResult{dev: dev, err: err}
	}()
	select {
	case <-done:
		_ = finalizeSession(cfg, "", 0)
		return 0, fmt.Errorf("recorder: session aborted before the device dial completed")
	case dr := <-dialCh:
		if dr.err != nil {
			_ = finalizeSession(cfg, "", 0)
			return 0, fmt.Errorf("recorder: dial device %s: %w", cfg.Addr, dr.err)
		}
		dev := dr.dev
		return runSessionCapture(dev, cfg, done)
	}
}

// runSessionCapture is the bracket engine over an injected device handle — the
// unit-testable core (a fake satisfies sessionDevice when no device is
// available): launch the on-device screenrecord, hold the session until done
// closes, then stop (SIGINT → finalize wait), pull the MP4 via the goadb GetFile
// path, and finalize the FINAL marker + evidence row. A stop-side failure still
// finalizes the row (with the artifact only if the pull produced one) and is
// returned — the provider's stop surfaces the honest outcome.
func runSessionCapture(dev sessionDevice, cfg RecorderConfig, done <-chan struct{}) (int64, error) {
	if err := startScreenrecord(dev, cfg, done); err != nil {
		_ = finalizeSession(cfg, "", 0)
		return 0, err
	}
	<-done
	size, stopErr := stopScreenrecord(dev, cfg)
	mp4 := cfg.artifactPath()
	var pullErr error
	if stopErr == nil {
		var n int64
		if n, pullErr = getFile(dev, cfg.remotePath(), mp4); pullErr == nil {
			size = n
		} else {
			size = 0 // no COMPLETED pull -> the row must not claim a host artifact
		}
	} else {
		size = 0
	}
	finalErr := finalizeSession(cfg, mp4, size)
	// The FIRST failure in the stop chain is the one that matters (the row is on
	// disk regardless); a finalize failure is fatal on its own.
	for _, e := range []error{stopErr, pullErr, finalErr} {
		if e != nil {
			return size, e
		}
	}
	return size, nil
}
