package adb

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/opencharly/plugin-adb/candy/plugin-adb/params"
	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/kit"
	pb "github.com/opencharly/spec/proto"
	"github.com/opencharly/spec/spec"
)

// provider.go is the out-of-process provider for BOTH capabilities the plugin serves
// (F1). Invoke branches on the request class: a "deploy" op drives the `deploy:android`
// SUBSTRATE lifecycle (deploy.go — gate on boot, install the host-preresolved apk specs);
// every other op is the `adb:` check VERB. For the verb, charly's host dispatches an
// `adb:` check step through the registry (ResolveVerb("adb") → this grpcProvider →
// Provider.Invoke) with the FULL #Op marshaled as params_json and a CheckEnv snapshot as
// env. Because the out-of-process verb path does NOT run a host-side matcher pipeline,
// invokeVerb OWNS the whole verdict: dispatch the method, then evaluate the
// stdout/stderr/exit_status matchers + artifact validators itself (via the shared sdk
// implementation — R3), and return the wire {status,message} the host decodes.

type provider struct{ pb.UnimplementedProviderServer }

// Invoke runs one operation for the plugin's capabilities. The plugin serves BOTH
// the `adb:` check verb AND the `deploy:android` SUBSTRATE (F1), distinguished by the
// request's class: a "deploy" op drives the substrate install lifecycle (deploy.go) or,
// for OpPreresolve (F6, FINAL/K5 unit 6a), the device+install-spec resolution
// (preresolve.go); every other op is the adb verb.
func (p provider) Invoke(ctx context.Context, req *pb.InvokeRequest) (*pb.InvokeReply, error) {
	if req.GetClass() == "deploy" {
		if req.GetOp() == sdk.OpPreresolve {
			return invokeAndroidPreresolve(ctx, req)
		}
		return invokeDeployAndroid(req)
	}
	return p.invokeVerb(ctx, req)
}

// invokeVerb runs one `adb:` verb operation. It decodes the full #Op + the typed
// plugin input + the env, skips in box mode (these probes need a running container
// with a host-mapped adb port), dispatches the method, and self-evaluates the
// matchers + artifact validators.
func (provider) invokeVerb(ctx context.Context, req *pb.InvokeRequest) (*pb.InvokeReply, error) {
	var op spec.Op
	if len(req.GetParamsJson()) > 0 {
		if err := json.Unmarshal(req.GetParamsJson(), &op); err != nil {
			return sdk.ResultJSON("fail", "adb: decode op: "+err.Error())
		}
	}
	var in params.AdbInput
	kit.DecodeInput(op.PluginInput, &in)
	var env adbEnv
	if len(req.GetEnvJson()) > 0 {
		_ = json.Unmarshal(req.GetEnvJson(), &env)
	}
	// The verb's method + per-verb fields ride the desugared plugin input since
	// the schema-compaction cutover; dispatch decodes the full typed params.AdbInput.
	method := kit.InputStr(&op, "method")

	// Live-container verb: skip under `charly check box` (no host-mapped adb port
	// on a disposable `podman run --rm`) — mirrors the host's RunModeBox/box-mode skip.
	if env.Mode == "box" {
		return sdk.ResultJSON("skip", fmt.Sprintf("adb: %s requires a running container (skip under charly check box)", method))
	}
	// No device context at all (no resolved adb addr, no container) → skip, the
	// check-verb analogue of the host's empty-box skip. The deploy/status
	// seams always set AdbAddr, so they never hit this.
	if env.AdbAddr == "" && env.inPodContainer() == "" {
		return sdk.ResultJSON("skip", fmt.Sprintf("adb: %s has no device context (box=%q)", method, env.Box))
	}

	// session (Cutover E, E-4): the DETACHED recorder owns the device wire — the
	// provider never dials for a session. The device-context resolution (container
	// inspect → host-published 5037) gates on the live deployment mirroring the
	// record-session contract; start hands the spawn to the runner's generic
	// background-session service (verb:session) over the InvokeProvider reverse
	// leg; stop/status talk to that same service. No artifact is produced inside
	// this Invoke (the recorder writes <session>.mp4 detached), so artifactMethod
	// stays false.
	if method == "session" {
		// The reverse leg needs a CheckContext (dialed once on the Invoke's broker).
		cc, cerr := sdk.NewCheckContext(req.GetExecutorBrokerId(), req.GetEnvJson())
		if cerr != nil {
			return sdk.ResultJSON("fail", fmt.Sprintf("adb: session: %v", cerr))
		}
		out, runErr := runSession(ctx, cc, &env, &in, env.Venue)
		return sdk.VerbVerdict("adb", method, out, runErr, &op, false)
	}

	out, runErr := dispatch(&env, &op)

	// The shared exit/stdout/stderr MATCHERS-only verdict pipeline (R3) — the
	// artifact validators left the verdict at the G-8 cutover: the screencap
	// artifact tail runs below through the shared sdk.LandArtifact entry point
	// (the write-then-validate call every capture plugin uses).
	reply, verr := sdk.VerbVerdict("adb", method, out, runErr, &op, false)
	if verr != nil {
		return nil, verr
	}
	// Gate the artifact tail on a PASS verdict — a matcher mismatch returns
	// before any artifact work.
	if status, _ := replyStatus(reply); status != "pass" {
		return reply, nil
	}
	// screencap is adb's one artifact-producing method. The PNG is written
	// HOST-side (the provider runs on the host via LocalTransport and dials the
	// device itself; the base64 PNG comes back over the goadb exec wire), so this
	// is the HOST leg of sdk.LandArtifact: nothing pulled, nothing written; the
	// shared artifact validators (artifact_min_bytes etc.) ALWAYS run on the
	// existing host artifact path via sdk.RunArtifactValidators.
	if method == "screencap" {
		if err := sdk.LandArtifact(ctx, nil, "", in.Artifact, &op); err != nil {
			return sdk.ResultJSON("fail", fmt.Sprintf("adb: screencap: %v", err))
		}
	}
	return reply, nil
}

// replyStatus decodes the {status,message} wire every out-of-process check verb
// returns (ResultJSON > InvokeReply.ResultJson; the host's pluginCheckResult
// reads the same shape). The provider uses it to gate the artifact tail on the
// shared verdict pipeline's outcome without duplicating the wire contract.
func replyStatus(reply *pb.InvokeReply) (status, message string) {
	if reply == nil || len(reply.GetResultJson()) == 0 {
		return "", ""
	}
	var w struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(reply.GetResultJson(), &w); err != nil {
		return "", ""
	}
	return w.Status, w.Message
}
