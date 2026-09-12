package adb

// g8_land_test.go — B12 proof for the G-8 changed path (PR #8): the `adb: screencap`
// method's `sdk.LandArtifact` tail over the REAL goadb wire (the same minimal adb
// server stub the recorder tests dial). The stub's shell serves a base64 1x1 PNG; the
// provider's screencap method decodes it to the HOST artifact path, the shared verdict
// pipeline passes, and the tail MUST run LandArtifact → RunArtifactValidators against
// the step's declared artifact_min_bytes — a 67-byte PNG fails the tail. Without the
// G-8 LandArtifact call the reply would come back a plain "pass" and this test fails;
// the non-screencap gate test pins that the tail is gated to the one artifact method.

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencharly/spec/ops"
	pb "github.com/opencharly/spec/proto"
	"github.com/opencharly/spec/spec"
)

// tinyPNGBase64 is a real 1x1 transparent PNG (67 bytes when decoded) — decodable by
// runScreencap's base64 round-trip, but far below any realistic artifact_min_bytes.
const tinyPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

// newStubListener starts the minimal goadb server stub on a fresh local port and
// returns the listener + the canned-shell state (serveStub runs in the background).
func newStubListener(t *testing.T, st *stubState) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go serveStub(ln, st)
	return ln
}

// invokeVerbAgainstStub drives the provider's invokeVerb end-to-end against the stub
// adb server (class "verb", method + artifact validators ride the plugin input; the
// env ships the stub address as the resolved device's adb server).
func invokeVerbAgainstStub(t *testing.T, ln net.Listener, input map[string]any) (*pb.InvokeReply, error) {
	t.Helper()
	return invokeVerbOpAgainstStub(t, ln, &spec.Op{PluginInput: input})
}

// invokeVerbOpAgainstStub is invokeVerbAgainstStub for a test that must ALSO pin
// top-level step fields: the stdout/stderr matcher lists and exit_status live on the
// step's Op, BESIDE plugin_input, exactly as the authored wire places them — not
// inside the plugin input map.
func invokeVerbOpAgainstStub(t *testing.T, ln net.Listener, op *spec.Op) (*pb.InvokeReply, error) {
	t.Helper()
	paramsJSON, err := json.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	envJSON, err := json.Marshal(map[string]any{"adb_addr": ln.Addr().String(), "mode": "live"})
	if err != nil {
		t.Fatal(err)
	}
	req := &pb.InvokeRequest{Class: "verb", ParamsJson: paramsJSON, EnvJson: envJSON}
	return (provider{}).invokeVerb(context.Background(), req)
}

// decodeWire decodes the {status,message} InvokeReply wire through the CONTRACT
// module's shared decoder — ops.ParseResultJSON, the counterpart of the encoder
// the verdict pipeline replies with — so these tests never re-declare the shape
// the plugin itself stopped re-declaring (R3).
func decodeWire(t *testing.T, reply *pb.InvokeReply) (status, message string) {
	t.Helper()
	status, message, err := ops.ParseResultJSON(reply)
	if err != nil {
		t.Fatalf("decode reply wire %q: %v", string(reply.GetResultJson()), err)
	}
	return status, message
}

// TestInvokeVerbScreencapLandArtifactTail is the changed-path execution over the
// real adb wire: screencap's artifact tail (sdk.LandArtifact on the host-side PNG)
// must run the shared validators. A 67-byte PNG against artifact_min_bytes=1024
// must FAIL with the promised "adb: screencap: <validator err>" wire shape — if the
// G-8 tail were missing, the matchers-only verdict would return "pass" and this
// test fails. A satisfiable minimum passes through the same tail.
func TestInvokeVerbScreencapLandArtifactTail(t *testing.T) {
	st := &stubState{shellOut: tinyPNGBase64, shellMu: make(chan int, 1)}
	ln := newStubListener(t, st)

	artifact := filepath.Join(t.TempDir(), "screencap.png")
	reply, err := invokeVerbAgainstStub(t, ln, map[string]any{
		"method":             "screencap",
		"artifact":           artifact,
		"artifact_min_bytes": 1024,
	})
	if err != nil {
		t.Fatalf("invokeVerb(screencap): %v", err)
	}
	status, msg := decodeWire(t, reply)
	if status != "fail" {
		t.Fatalf("screencap tail did not run the artifact validators: status=%q msg=%q (want fail)", status, msg)
	}
	if !strings.Contains(msg, "adb: screencap:") || !strings.Contains(msg, "min_bytes") {
		t.Fatalf("wire message %q does not match the promised adb: screencap: <validator err> shape", msg)
	}
	// runScreencap landed the PNG host-side before the tail validated it — the
	// host-side artifact path the LandArtifact no-pull leg validates.
	if _, err := os.Stat(artifact); err != nil {
		t.Fatalf("host artifact %q missing after screencap: %v", artifact, err)
	}

	// Control: a satisfiable minimum flows through the same tail to "pass".
	reply2, err := invokeVerbAgainstStub(t, ln, map[string]any{
		"method":             "screencap",
		"artifact":           filepath.Join(t.TempDir(), "screencap2.png"),
		"artifact_min_bytes": 1,
	})
	if err != nil {
		t.Fatalf("invokeVerb(screencap control): %v", err)
	}
	if status2, _ := decodeWire(t, reply2); status2 != "pass" {
		t.Fatalf("control screencap status = %q, want pass", status2)
	}
}

// TestInvokeVerbNonScreencapSkipsArtifactTail pins the gate: only method ==
// "screencap" reaches the LandArtifact tail. A non-artifact method carrying a
// failing artifact_min_bytes must still return "pass" — proving the matchers-only
// verdict path (artifact=false) plus the method gate, not an artifact-validators
// run on every verb.
func TestInvokeVerbNonScreencapSkipsArtifactTail(t *testing.T) {
	st := &stubState{shellOut: "0", shellMu: make(chan int, 1)}
	ln := newStubListener(t, st)

	reply, err := invokeVerbAgainstStub(t, ln, map[string]any{
		"method":             "getprop",
		"property":           "sys.boot_completed",
		"artifact":           filepath.Join(t.TempDir(), "never-written.png"),
		"artifact_min_bytes": 1024,
	})
	if err != nil {
		t.Fatalf("invokeVerb(getprop): %v", err)
	}
	status, msg := decodeWire(t, reply)
	if status != "pass" {
		t.Fatalf("non-screencap method must skip the artifact tail: status=%q msg=%q (want pass)", status, msg)
	}
}

// TestInvokeVerbNonPassVerdictSkipsArtifactTail pins the verdict GATE itself: when
// the shared matcher pipeline already returns a NON-pass verdict, invokeVerb
// returns THAT wire before any artifact work. The step carries an artifact minimum
// the stub PNG cannot satisfy, so the two possible outcomes are distinguishable in
// the message — the stdout mismatch means the tail never ran, while the tail's own
// "required min" error means it did. Dropping the gate (running the tail
// unconditionally) fails this test on the 4000x4000 error.
func TestInvokeVerbNonPassVerdictSkipsArtifactTail(t *testing.T) {
	st := &stubState{shellOut: tinyPNGBase64, shellMu: make(chan int, 1)}
	ln := newStubListener(t, st)

	reply, err := invokeVerbOpAgainstStub(t, ln, &spec.Op{
		PluginInput: map[string]any{
			"method":                  "screencap",
			"artifact":                filepath.Join(t.TempDir(), "gated.png"),
			"artifact_min_dimensions": "4000x4000",
		},
		Stdout: spec.MatcherList{{Op: "contains", Value: "NEVER-APPEARS-IN-THE-SCREENCAP-OUTPUT"}},
	})
	if err != nil {
		t.Fatalf("invokeVerb(screencap): %v", err)
	}
	status, msg := decodeWire(t, reply)
	if status != "fail" {
		t.Fatalf("matcher verdict = %q (%q), want fail", status, msg)
	}
	if !strings.Contains(msg, "stdout:") {
		t.Fatalf("wire message %q is not the stdout matcher failure", msg)
	}
	if strings.Contains(msg, "4000x4000") {
		t.Fatalf("the artifact tail ran despite a non-pass verdict: %q", msg)
	}
}
