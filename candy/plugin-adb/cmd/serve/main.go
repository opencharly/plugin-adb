// Command serve is the OUT-OF-PROCESS entrypoint for the adb verb + deploy plugin: a
// thin shim serving the importable provider over go-plugin gRPC via sdk.Serve. The SAME
// NewProvider()/NewMeta() compile INTO charly in-process when listed in
// compiled_plugins; this binary is host-built + connected only when they are NOT —
// placement is invisible above the registry.
//
// HIDDEN RECORDER MODE (plan Cutover E, E-4): with CHARLY_ADB_RECORDER=1 the SAME
// binary skips serving and becomes the DETACHED session recorder — the runner's
// generic background-session service spawns it for an adb: session start. It dials
// the device from the resolved endpoint env, launches the ON-DEVICE screenrecord
// bracket, and on SIGTERM/SIGINT stops it, pulls the finalized MP4 via the goadb
// GetFile path into $CHARLY_ADB_STATE_DIR/<session>.mp4, and finalizes (FINAL
// marker + evidence row.json) before exiting 0: the runner's stop is complete only
// when row.json is on disk.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	adb "github.com/opencharly/plugin-adb/candy/plugin-adb"
	"github.com/opencharly/sdk"
)

func main() {
	if os.Getenv(adb.EnvRecorder) == "1" {
		os.Exit(recorderMain())
	}
	sdk.Serve(adb.NewProvider(), adb.NewMeta())
}

// recorderMain is the detached recorder process entrypoint (see the package doc). It
// returns the process exit code.
func recorderMain() int {
	addr := os.Getenv(adb.EnvAddr)
	stateDir := os.Getenv(adb.EnvStateDir)
	sessionID := os.Getenv(adb.EnvSessionID)
	if addr == "" || stateDir == "" || sessionID == "" {
		fmt.Fprintf(os.Stderr, "charly-adb recorder: missing env (addr=%q state_dir=%q session_id=%q)\n", addr, stateDir, sessionID)
		return 2
	}
	cfg := adb.RecorderConfig{
		Addr:      addr,
		Serial:    os.Getenv(adb.EnvSerial),
		StateDir:  stateDir,
		SessionID: sessionID,
		Venue:     os.Getenv(adb.EnvVenue),
		Phase:     os.Getenv(adb.EnvPhase),
	}

	done := make(chan struct{})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sig
		close(done) // deterministic finalize: device SIGINT → MP4 pull → FINAL + row.json
	}()

	bytes, err := adb.RunSessionRecorder(cfg, done)
	if err != nil {
		fmt.Fprintf(os.Stderr, "charly-adb recorder: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "charly-adb recorder: finalized session %s bytes=%d state_dir=%s\n", sessionID, bytes, stateDir)
	return 0
}
