# AGENTS.md — plugin-adb

Standalone out-of-tree plugin repo for the `adb` capability (`verb:adb` +
`deploy:android`). The plugin is a Go module at `candy/plugin-adb/` (module path
`github.com/opencharly/plugin-adb/candy/plugin-adb`); the root `charly.yml` only
declares `discover: candy` so the repo is a project and its candy is scanned.

Canonical files:

- `candy/plugin-adb/charly.yml` — the `plugin-adb:` candy entity (`plugin:` block,
  `plan:` checks).
- `candy/plugin-adb/` — the Go source: `plugin.go`/`provider.go`/`methods.go`,
  `schema/adb.cue`, `params/cue_types_gen.go`, `cmd/serve/main.go`.
- `.github/workflows/tag-on-merge.yml` — CalVer tag + `CHANGELOG/` on merge.
- `README.md` — user overview only; never agent guidance.

## Load these skills first (R0)

- `/charly-internals:plugin` — the plugin authoring reference: the `plugin:` block,
  the unified Provider model, the per-plugin CUE-schema contract, placement. Load
  before touching any provider or schema.
- `/charly-check:adb` — the `adb:` check verb reference (the plugin's user-facing
  surface). Load before changing a verb method or its input schema.
- `/charly-check:check` — the declarative check-step surface the `adb:` verb is
  authored through.
- `/charly-internals:git-workflow` — before any git/PR action.

## Build / validate / test

- `go build ./...` in `candy/plugin-adb/` — compile the plugin module.
- `go test ./...` in `candy/plugin-adb/` — the plugin's Go tests.
- `charly box validate` at the repo root — the structural check (the candy +
  `plugin:` block, CUE schema).
- The merge gate is the **org-wide** `charly/pr-validator` (required check
  `validate / validate`, defined in `opencharly/.github`); this repo has **no**
  per-repo candy gate.
- The live R10 witness is the `check-android-emulator-pod` bed in
  `opencharly/charly` (via `android-emulator-layer`).

## Modify this repo

- Edit the `plugin-adb:` candy entity, the Go source, and the `schema/adb.cue`
  input schema **together** — the schema is the single source for the verb's
  `params/` struct, so a method change that is not mirrored in the schema desyncs
  the generated types.
- The provider surface (the verbs/kinds it contributes) is the contract the
  README describes; keep them in step.

## Landing

- PR-only. Every change lands through a pull request; the org-required
  `charly/pr-validator` validates the diff and body and arms native auto-merge on
  PASS. Direct pushes to `main` are blocked.
- History lives in `CHANGELOG/` (written by `tag-on-merge` at merge time); the PR
  body IS the changelog.
- The authoritative rulebook is the umbrella `AGENTS.md` in
  `opencharly/opencharly` and `charly/AGENTS.md` in the charly repo. Do not
  restate its rules here.
