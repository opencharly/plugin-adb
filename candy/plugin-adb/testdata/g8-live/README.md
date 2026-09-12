# G-8 live verification assets (screencap -> sdk.LandArtifact)

The two step files that FORMALISE the checks for the `adb: screencap` tail landed through
`sdk.LandArtifact` (plan G-8). They are committed so the live proof is reproducible instead of
living in `/tmp` — and so the invocation, its exit codes and its provenance travel with the PR.

**These files ARE the executed form.** Both were run UNMODIFIED (sha256 identical before and
after the run) on the isolated-invocation bed `g8iso-live` (see *Target* below), through the
invocation in the last section. The **exit code is the assertion**:

| file | ids it carries | own exit code | its own output |
|---|---|---|---|
| `screencap-live-steps.yml` | `g8-devices-online`, `g8-screencap-passes`, `g8-landed-file-present` | **0** | `3 steps: 3 passed, 0 failed, 0 skipped` |
| `screencap-negative-control-steps.yml` | `g8-neg-screencap` | **2** | `1 step: 0 passed, 1 failed` naming `required min 4000x4000` |

The positive run's own line names the changed tail's work (`wrote <N> bytes to
/tmp/g8-land-spike.png` — the real emulator PNG is ~1.39 MB and the byte count varies per run);
the negative run's own line is the changed tail's own rejection message. Each run pastes the
`charly version` of the binary that produced it beside those exit codes.

**Which bed is this change's R10 gate.** It is `g8iso-live` — the `Target` below. It
deliberately carries NO baked plan: `--steps-file` runs ONLY the injected steps, so a baked
plan would never execute, and the bed's gate is exactly these two files. `spike-g8-land` (the
scratch bed used while developing the cutover) is a DIFFERENT target with a composed, baked plan
— see *Named batches* at the bottom; its state is not what these files assert, and its step ids
are not these ids.

**Authoring form — read this before editing these files.** These step files are written in the
**WIRE form** — `plugin:` + `plugin_input:` + explicit `op:`/`value:` matchers — and NOT in
the `<word>: <input>` authoring sugar.

**Why, proven live on 2026-09-12:** `charly check live <bed> --steps-file <file>` parses the
file with a raw `yaml.Unmarshal` into `[]spec.Step` (`plugin-check/candy/plugin-check/live_cmd.go`),
and `spec.Op` carries no per-verb fields, no catch-all and no `UnmarshalYAML`. The sugar is
therefore **silently dropped** — the step then reports `check has no verb set` and exits 2 while
proving **nothing** — or it aborts the whole decode (`command:` is a map, `spec.Op.Command` is a
Go string, so `--steps-file` exits 1). The desugar exists only in the loader
(`sdk/loaderkit/parse.go`), and plugin-check's CHANGELOG records its omission on this path as
deliberate for **engine-injected** steps. `--steps-file` is however a **user-facing** lever, and
the authoring sugar is what the plan docs teach, so the divergence is routed as batch **G-6a**
(*Named batches* below) rather than left as a footnote. Until that lever accepts the sugar, a
hand-authored steps file **must** use the wire form above.

## Target

The bed these assets run against — a disposable pod composing the `android-emulator` image,
with NO `plan:` (the injected steps ARE the gate):

```yaml
g8iso-live:
    pod:
        image: android-emulator
        disposable: true
        lifecycle: dev
        add_candy:
            - '@github.com/opencharly/plugin-adb/candy/plugin-adb'   # VERSIONLESS on purpose
```

`add_candy` is VERSIONLESS so the checker resolves `plugin-adb` through
`CHARLY_REPO_OVERRIDE` and builds the provider binary FRESH for the run rather than serving a
merged tag. The run log then carries the provenance line

    plugin plugin-adb: using LOCAL OVERRIDE <this checkout> — serving <cache>/plugin-adb-<id> (build stamp <id>); source <this checkout>/candy/plugin-adb

The build stamp is a sha256 over the candy's own source tree (plus the module versions its
`go.mod` pins — `charly/charly/plugin_build_stamp.go`), so it identifies the served source.

## Invocation

```sh
cd <the project holding the bed>
CHARLY_REPO_OVERRIDE=github.com/opencharly/plugin-adb=<this checkout> \
  charly check live g8iso-live --steps-file <this dir>/screencap-live-steps.yml            ; echo "exit=$? (want 0)"
CHARLY_REPO_OVERRIDE=github.com/opencharly/plugin-adb=<this checkout> \
  charly check live g8iso-live --steps-file <this dir>/screencap-negative-control-steps.yml; echo "exit=$? (want 2)"
```

Both invocations target the SAME disposable pod; run them one at a time (each
`charly check live` attaches its own adb session to the bed's emulator). A readiness gate
(`adb wait-for-device` + `adb wait-ui-settled`, also in the wire form) belongs BEFORE them, so
"the venue is not ready yet" can never be read as "the asset failed".

## Named batches

Two findings these assets surfaced, each named with its owner — neither is a parking phrase:

- **G-6a — the `--steps-file` authoring-form divergence** (owner: `plugin-check`, plus the
  plan-docs owner). The exit: teach that user-facing lever the same parse-time desugar the loader
  uses (`sdk/loaderkit/parse.go`), or have it REJECT the sugar loudly instead of silently
  dropping the verb.
- **G-5a — composed-ref bed fixture anchoring** (owners: `charly`/`spec` host side, plus the
  bed owner). A relative committed-APK fixture cannot anchor in a pod composed purely from
  `@github` refs, because the fixture-owning candy is absent from the running candy scan
  (`spec/checkhost/apk.go`: "absent from the source scan (0 candies scanned) — cannot anchor the
  fixture"; charly's host side is the only place that can fill that map for an out-of-process
  verb: "an out-of-process verb has no CandyDirs, so it cannot anchor the fixture itself").
  This class belongs to the composed `spike-g8-land` bed, NOT to these assets.
