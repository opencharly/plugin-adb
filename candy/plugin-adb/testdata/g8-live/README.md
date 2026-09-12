# G-8 live verification assets (screencap -> sdk.LandArtifact)

The two step files that FORMALISE the checks that ran against a live disposable bed
while landing `sdk.LandArtifact` for the `adb: screencap` tail (plan G-8). They are
committed so the live proof becomes reproducible instead of living in `/tmp`.

**Executed or not — stated plainly, because an earlier revision overclaimed it.** The
evidence pasted in PR #8 comes from the SPIKE bed's own inline step list, whose ids are
`g8-devices-online` / `g8-screencap-passes` / `g8-landed-file-present` /
`g8-screencap-validator-fails`. THESE files carry the ids `g8-devices-online` /
`g8-screencap-passes` / `g8-landed-file-present` / **`g8-neg-screencap`**, and they
express the negative control as an ISOLATED invocation (`charly check live <bed>
--steps-file <file>`, where the **exit code is the assertion**: `0` = the check ran and
passed, `2` = it ran and failed as required) rather than as an inline step whose wrapper
swallows the exit. **They have not yet been executed in this exact form.** Running them is
what produces the version-matched, self-contained live proof R10 still owes: the run
pastes its own `charly version` beside its own exit code.

| file | what it proves | assertion |
|---|---|---|
| `screencap-live-steps.yml` | the changed tail runs host-side against a booted emulator and its validators ACCEPT the real PNG | `charly check live` exits 0 |
| `screencap-negative-control-steps.yml` | the SAME tail REJECTS an impossible `artifact_min_dimensions` | `charly check live` exits 2 and the report names `required min 4000x4000` |

## Authoring form (read this before editing these files)

These step files are written in the **WIRE form** — `plugin:` + `plugin_input:` + explicit
`op:`/`value:` matchers — and NOT in the `<word>: <input>` authoring sugar.

**Why, proven live on 2026-09-12:** `charly check live <bed> --steps-file <file>` parses the
file with a raw `yaml.Unmarshal` into `[]spec.Step`
(`plugin-check/candy/plugin-check/live_cmd.go:39-47`), and `spec.Op` carries no per-verb
fields, no catch-all and no `UnmarshalYAML`. The sugar is therefore **silently dropped** —
the step then reports `check has no verb set` and exits 2 while proving **nothing** — or it
aborts the whole decode (`command:` is a map, `spec.Op.Command` is a Go string). The desugar
exists only in the loader (`sdk/loaderkit/parse.go`), and plugin-check's CHANGELOG records
its omission on this path as deliberate for **engine-injected** steps.

`--steps-file` is however a **user-facing** lever, and the authoring sugar is what the plan
docs teach — so this divergence is tracked as its own finding. Until that lever accepts the
sugar, a hand-authored steps file **must** use the wire form above.

The assertion mechanism is unchanged and is the reason these files exist: the **exit code is
the assertion** (`0` = ran and passed; `2` = ran and failed as required, with the failure
message naming the reason).

## Target

A disposable pod bed composing the `android-emulator` image:

```yaml
spike-g8-land:
    pod:
        image: android-emulator
        disposable: true
        lifecycle: dev
        add_candy:
            - '@github.com/opencharly/plugin-adb/candy/plugin-adb'   # VERSIONLESS on purpose
```

`add_candy` is VERSIONLESS so the checker resolves `plugin-adb` through
`CHARLY_REPO_OVERRIDE` and builds the provider binary FRESH for the run rather than
serving a merged tag. The run log then carries the provenance line

    plugin plugin-adb: served from <cache>/plugin-adb-<id> (build stamp <id>); source <this checkout>/candy/plugin-adb

## Invocation

```sh
cd <the project holding the bed>
CHARLY_REPO_OVERRIDE=github.com/opencharly/plugin-adb=<this checkout> \
  charly check live <bed> --steps-file <this dir>/screencap-live-steps.yml            ; echo "exit=$? (want 0)"
CHARLY_REPO_OVERRIDE=github.com/opencharly/plugin-adb=<this checkout> \
  charly check live <bed> --steps-file <this dir>/screencap-negative-control-steps.yml; echo "exit=$? (want 2)"
```

Both invocations target the SAME disposable pod; run them one at a time (each
`charly check live` attaches its own adb session to the bed's emulator).
