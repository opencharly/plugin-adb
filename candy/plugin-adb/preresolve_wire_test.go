package adb

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencharly/spec/spec"
)

// TestAndroidPreresolveParamsWireRoundTrip reproduces the check-android-emulator-pod
// bring-up failure: the host serializes the install plans as spec.InstallPlanView
// (the JSON-roundtrippable wire form — build_overlay.go/unified_targets.go), but the
// preresolve params decoded them as *deploykit.InstallPlan, whose Steps
// []spec.InstallStep interface cannot unmarshal from the wire object:
//
//	json: cannot unmarshal object into androidPreresolveParams.plans.0.steps.0 of type spec.InstallStep
func TestAndroidPreresolveParamsWireRoundTrip(t *testing.T) {
	repo := t.TempDir()
	candyDir := filepath.Join(repo, "candy", "layer-android-test-apps")
	if err := os.MkdirAll(candyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	apk := filepath.Join(repo, "tests", "data", "fdroid.apk")
	if err := os.MkdirAll(filepath.Dir(apk), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(apk, []byte("PK"), 0o644); err != nil {
		t.Fatal(err)
	}

	plan := &spec.InstallPlan{
		DeployID: "test-deploy",
		Box:      "android-emulator",
		Steps: []spec.InstallStep{
			&spec.ApkInstallStep{
				CandyName: "layer-android-test-apps",
				CandyDir:  candyDir,
				Packages:  []spec.ApkPackageSpec{{Apk: "tests/data/fdroid.apk"}, {Package: "org.fdroid.fdroid"}},
			},
		},
	}
	wv := spec.WireView(plan)
	params := androidPreresolveParams{Plans: []*spec.InstallPlanView{&wv}}

	b, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal host wire: %v", err)
	}
	var back androidPreresolveParams
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("decode host wire (the production failure): %v", err)
	}
	installs, err := collectAndroidInstalls(back.Plans)
	if err != nil {
		t.Fatalf("collect android installs: %v", err)
	}
	if len(installs) != 2 {
		t.Fatalf("got %d installs, want 2", len(installs))
	}
	if installs[0].Apk != apk {
		t.Errorf("installs[0].Apk = %q, want absolute %q", installs[0].Apk, apk)
	}
	if installs[1].Package != "org.fdroid.fdroid" {
		t.Errorf("installs[1].Package = %q, want org.fdroid.fdroid", installs[1].Package)
	}
}
