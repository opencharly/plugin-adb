# G-8 live verification assets (screencap -> sdk.LandArtifact)

The two step files that were RUN against a live disposable bed while landing
`sdk.LandArtifact` for the `adb: screencap` tail (plan G-8). They are committed so
the live proof stays reproducible instead of living in `/tmp`.

| file | what it proves | assertion |
|---|---|---|
| `screencap-live-steps.yml` | the changed tail runs host-side against a booted emulator and its validators ACCEPT the real PNG | `charly check live` exits 0 |
| `screencap-negative-control-steps.yml` | the SAME tail REJECTS an impossible `artifact_min_dimensions` | `charly check live` exits 2 and the report names `required min 4000x4000` |

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
