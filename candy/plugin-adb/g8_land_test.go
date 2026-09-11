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

	"github.com/opencharly/sdk"
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
	op := &spec.Op{PluginInput: input}
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

// decodeWire decodes the {status,message} InvokeReply wire the changed replyStatus
// gate reads, failing the test on an undecodable payload.
func decodeWire(t *testing.T, reply *pb.InvokeReply) (status, message string) {
	t.Helper()
	var w struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(reply.GetResultJson(), &w); err != nil {
		t.Fatalf("decode reply wire %q: %v", string(reply.GetResultJson()), err)
	}
	return w.Status, w.Message
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

// TestReplyStatusWireDecode covers the replyStatus gate's decode contract: nil,
// empty, and non-JSON payloads decode to zero values (no panic), and the real
// ResultJSON wire round-trips status+message.
func TestReplyStatusWireDecode(t *testing.T) {
	if s, _ := replyStatus(nil); s != "" {
		t.Fatalf("nil reply status = %q, want empty", s)
	}
	if s, _ := replyStatus(&pb.InvokeReply{}); s != "" {
		t.Fatalf("empty payload status = %q, want empty", s)
	}
	if s, _ := replyStatus(&pb.InvokeReply{ResultJson: []byte("not-json")}); s != "" {
		t.Fatalf("garbage payload status = %q, want empty", s)
	}
	reply, _ := sdk.ResultJSON("pass", "wrote 67 bytes to /tmp/screencap.png")
	if s, m := replyStatus(reply); s != "pass" || m != "wrote 67 bytes to /tmp/screencap.png" {
		t.Fatalf("wire round-trip = (%q, %q), want (pass, wrote 67 bytes ...)", s, m)
	}
	replyFail, _ := sdk.ResultJSON("fail", "adb: screencap: artifact size 67 < required min_bytes 1024")
	if s, m := replyStatus(replyFail); s != "fail" || !strings.Contains(m, "adb: screencap:") {
		t.Fatalf("fail wire round-trip = (%q, %q)", s, m)
	}
}
