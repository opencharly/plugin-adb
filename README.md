# plugin-adb

Android Debug Bridge interaction for OpenCharly — the `adb:` check verb, the
`target: android` app-install deploy, and the goadb-backed device probe behind
`charly status`.

The plugin is an out-of-tree Go module: charly fetches this repo at the pinned
tag, go-builds the provider on the host, and serves it **out-of-process** over
go-plugin gRPC via the plugin SDK. That keeps the
`github.com/zach-klippenstein/goadb` dependency and the single apk-install path
(`apkeep` + `adb`) out of charly's core `go.mod`, while `adb:` authoring stays
unchanged — the verb dispatches through the provider registry exactly like a
built-in.

## What it provides

| Capability | Surface |
|---|---|
| `verb:adb` | the `adb:` check verb — 13 methods: `devices`, `shell`, `install`, `install-app`, `uninstall`, `getprop`, `screencap`, `logcat-tail`, `wait-for-device`, `wait-ui-settled`, `current-focus`, `keyevent`, `session` |
| `deploy:android` | the `target: android` app-install deploy substrate |

The `session` method is an on-device screen-recording bracket: the plugin's own
binary runs in recorder mode through the runner's generic background-session
service, starts `screenrecord` at phase start, stops it at phase end, and pulls
the MP4 into the evidence row.

## How to use it

Compose the plugin candy in a box or check bed's `candy:` list:

```yaml
- '@github.com/opencharly/plugin-adb/candy/plugin-adb:<tag>'
```

Then author the verb in a plan:

```yaml
- check: the emulator is attached to the adb server
  id: adb-devices-online
  adb: devices
  stdout:
    - contains: emulator-5554
  context: [runtime]
```

## Layout

- `candy/plugin-adb/` — the plugin module: `plugin.go` (provider + meta),
  `methods.go`, `session_method.go`, `device.go`, `install.go`, `deploy.go`,
  `preresolve.go`, `schema/adb.cue` (the self-contained `#AdbInput`),
  `params/cue_types_gen.go`, and `cmd/serve/main.go`.
- `charly.yml` — the root project manifest (`discover: candy`).
- `.github/workflows/tag-on-merge.yml` — CalVer tag + `CHANGELOG/` on merge.

## Related

- Owning skill: `/charly-check:adb` — the `adb:` check verb reference (the candy
  carries no `skill:` entity of its own; the gap is tracked in
  [opencharly/opencharly#291](https://github.com/opencharly/opencharly/issues/291)).
- `/charly-internals:plugin` — the plugin/provider model.
- [`opencharly/charly`](https://github.com/opencharly/charly) — the charly CLI.
