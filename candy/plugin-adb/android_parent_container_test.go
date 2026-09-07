package adb

import (
	"strings"
	"testing"

	"github.com/opencharly/spec/spec"
)

// TestAndroidParentContainer proves the nested-device container derivation:
// the loader stamps Deploy.MemberOf with the folded parent's registered key, which
// the leaf-name preresolve path relies on (the dotted-path form is the fallback).
func TestAndroidParentContainer(t *testing.T) {
	// folded member node (the bed's nested device: under check-android-emulator-pod)
	n := &spec.Deploy{MemberOf: "check-android-emulator-pod"}
	if got := androidParentContainer(n, "device"); got != "charly-check-android-emulator-pod" {
		t.Errorf("MemberOf path: got %q, want charly-check-android-emulator-pod", got)
	}
	// dotted-path fallback (un-stamped caller)
	if got := androidParentContainer(nil, "stack.web.device"); got != "charly-stack_web" {
		t.Errorf("dotted fallback: got %q, want charly-stack_web", got)
	}
	// standalone device: no member, no dot
	if got := androidParentContainer(nil, "device"); got != "" {
		t.Errorf("standalone: got %q, want empty", got)
	}
}

// TestResolveAndroidHostPortRefErrors proves the ${HOST_PORT:N} ref's PURE error
// paths: leaves non-template addrs untouched, rejects malformed/zero ports, and
// reports a clear error when the device has no parent pod. The live parent-
// container derivation (MemberOf-stamped members like the bed's device-net)
// is covered by TestAndroidParentContainer + the E-4 R10 bed run; the OLD inline
// parse only understood dotted paths, so a MemberOf-stamped leaf member with no
// dots ("device-net") wrongly reported "not nested under a pod" even though the
// loader had stamped its parent — that regression is what the MemberOf-aware
// derivation fixes (resolver now shares androidParentContainer, R3).
func TestResolveAndroidHostPortRefErrors(t *testing.T) {
	if got, err := resolveAndroidHostPortRef("127.0.0.1:5037", "device-net", nil); err != nil || got != "127.0.0.1:5037" {
		t.Fatalf("plain addr: got %q, err %v", got, err)
	}
	if _, err := resolveAndroidHostPortRef("127.0.0.1:${HOST_PORT:5037", "device-net", nil); err == nil {
		t.Fatal("malformed template (no closing brace): want error")
	}
	if _, err := resolveAndroidHostPortRef("127.0.0.1:${HOST_PORT:0}", "device-net", nil); err == nil {
		t.Fatal("zero container port: want error")
	}
	if _, err := resolveAndroidHostPortRef("127.0.0.1:${HOST_PORT:-1}", "device-net", nil); err == nil {
		t.Fatal("negative container port: want error")
	}
	// standalone endpoint device: path has no dots and node carries no MemberOf
	// stamp → no parent pod container to read the published port from.
	_, err := resolveAndroidHostPortRef("127.0.0.1:${HOST_PORT:5037}", "device-net", nil)
	if err == nil {
		t.Fatal("standalone endpoint: want no-parent error")
	}
	if !strings.Contains(err.Error(), "not nested under a pod") {
		t.Fatalf("standalone endpoint: want no-parent error, got %v", err)
	}
}
