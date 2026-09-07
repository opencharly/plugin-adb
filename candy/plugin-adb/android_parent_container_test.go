
package adb

import (
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
