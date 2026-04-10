# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository layout

SiYuan is a two-process application:

- [kernel/](kernel/) — Go backend ("kernel process"). Owns all data, persistence, indexing, sync, export/import, and business logic. Exposes an HTTP + WebSocket API (default port 6806) that every frontend talks to.
- [app/](app/) — TypeScript frontend (Electron desktop, mobile WebView, and browser). All rendering and editing lives here; everything stateful round-trips through the kernel API.
- [scripts/](scripts/) — release/packaging helpers (Windows/macOS/Linux build scripts, changelog parsers, language-key checker).

The two halves communicate purely over `http://127.0.0.1:6806/api/...` and a `/ws` WebSocket. In dev you run them as two separate processes and point Electron at the locally-built kernel binary.

## Kernel (Go)

Entry point: [kernel/main.go](kernel/main.go). The boot sequence is intentional and order-sensitive:
`util.Boot` → `model.InitConf` → `server.Serve` (async) → `InitAppearance` → `sql.InitDatabase`/`InitHistoryDatabase`/`InitAssetContentDatabase` → `model.BootSyncData` → `model.InitBoxes` → `model.LoadFlashcards` → `util.SetBooted` → `job.StartCron` → asset/emoji/theme watchers → `HandleSignal`.

Key packages:

- [kernel/api/](kernel/api/) — Gin route handlers. [kernel/api/router.go](kernel/api/router.go) is the single source of truth for every endpoint, split into "no auth required" and "auth required" sections. Auth/role/readonly middleware (`model.CheckAuth`, `model.CheckAdminRole`, `model.CheckReadonly`) is applied per-route. When adding an endpoint, register it here and implement the handler in the appropriate `api/*.go` file; put business logic in [kernel/model/](kernel/model/), not in the handler.
- [kernel/model/](kernel/model/) — Business logic: blocks, transactions, attribute views, sync, repository (dejavu), export/import, bazaar, flashcards, OCR, AI, etc. This is where most feature work lands.
- [kernel/sql/](kernel/sql/) — SQLite persistence with FTS5. There are three databases (main, history, asset-content). Writes go through an async queue ([kernel/sql/queue.go](kernel/sql/queue.go)); do not `INSERT`/`UPDATE` directly on the main path — enqueue instead. The `fts5` build tag is mandatory.
- [kernel/treenode/](kernel/treenode/) — In-memory block tree / blocktree index. Wraps the [88250/lute](https://github.com/88250/lute) markdown engine, which is the authoritative Markdown AST implementation; do not reinvent parsing, route through `lute`.
- [kernel/filesys/](kernel/filesys/) — On-disk persistence of `.sy` (per-document JSON) files and templates. Block data lives in `workspace/data/<notebook>/...`.
- [kernel/av/](kernel/av/) — Attribute Views (the database/table/gallery/kanban feature). `av.go` holds the core types; `layout_*.go` are per-view-type projections.
- [kernel/util/](kernel/util/) — Boot, session, crypto, paths, OCR, Lute helpers, OS-specific shims. Platform-specific files use build tags (`_darwin.go`, `_windows.go`, `_mobile.go`).
- [kernel/server/](kernel/server/) — Gin server wiring, TLS, reverse-proxy support.
- [kernel/mobile/](kernel/mobile/) — `gomobile` entry point for iOS/Android; excluded from desktop builds via `//go:build mobile` / `!mobile`.
- [kernel/harmony/](kernel/harmony/) — HarmonyOS build (requires patched Go toolchain, see [.github/CONTRIBUTING.md](.github/CONTRIBUTING.md)).

### Build / run kernel

Requires Go (see `go 1.25.x` in [kernel/go.mod](kernel/go.mod)) and `CGO_ENABLED=1`. The `fts5` build tag is required for SQLite full-text search.

```bash
cd kernel
# Linux/macOS
go build -tags "fts5" -o "../app/kernel/SiYuan-Kernel"
# Windows
go build -tags "fts5" -o "../app/kernel/SiYuan-Kernel.exe"

# Run in dev mode (must be started manually before the Electron frontend)
cd ../app/kernel
./SiYuan-Kernel --wd=.. --mode=dev
```

Mobile builds use `gomobile bind -tags fts5 ... ./mobile/` targeting `ios` or `android/arm64`. HarmonyOS uses `kernel/harmony/build.sh` and requires Go source patches documented in CONTRIBUTING.

Tests are sparse and colocated (`*_test.go`). Run them with `go test -tags fts5 ./...` from `kernel/`. Don't add new tests that hard-code paths outside `kernel/testdata/`.

## Frontend (app/)

Package manager is **pnpm 10.33.0** pinned via `packageManager`. Electron version is pinned to **40.8.5** and must be installed explicitly: `pnpm install electron@40.8.5 -D`.

Entry point: [app/electron/main.js](app/electron/main.js) (Electron main process) loads [app/src/index.ts](app/src/index.ts) (renderer).

Frontend scripts (run from [app/](app/)):

```bash
pnpm run lint            # eslint --fix --cache
pnpm run dev             # webpack dev, default (web/browser bundle)
pnpm run dev:desktop     # webpack dev, desktop bundle (webpack.desktop.js)
pnpm run dev:mobile      # webpack dev, mobile bundle (webpack.mobile.js)
pnpm run dev:export      # webpack dev, export bundle (webpack.export.js)
pnpm run build           # runs all build:* scripts in parallel
pnpm run start           # launches Electron against ./electron/main.js
pnpm run gen:types       # tsc -d (emit .d.ts only)
pnpm run dist[-linux|-darwin|-arm64|...]  # electron-builder packaging
```

There are **four webpack bundles** — `webpack.config.js` (web), `webpack.desktop.js`, `webpack.mobile.js`, `webpack.export.js`. Use `ifdef-loader` `/// #if` guards to fork code across them instead of runtime branches when a feature is build-target-specific. ESLint ignores `build`, `node_modules`, `src/asset/pdf`, `src/types/dist`, `stage`, and `appearance`.

Frontend module map ([app/src/](app/src/)):

- [protyle/](app/src/protyle/) — **The block-based WYSIWYG editor.** This is the largest and most central module. Sub-areas: `wysiwyg/` (DOM + input), `toolbar/`, `hint/`, `render/`, `gutter/`, `breadcrumb/`, `undo/`, `upload/`, `export/`. Most editor bugs and features touch this tree. `protyle/method.ts` and `protyle/index.ts` are the public surface.
- [layout/](app/src/layout/) — Window/tab/dock model (`Wnd.ts`, `Tab.ts`, `Model.ts`, `dock/`, `topBar.ts`). Owns the tab-splitting UI and layout serialization.
- [boot/](app/src/boot/) — Startup sequencing, config hydration (`onGetConfig.ts`), changelog display, compatibility checks.
- [menus/](app/src/menus/), [dialog/](app/src/dialog/), [search/](app/src/search/), [config/](app/src/config/) — UI surfaces. Self-explanatory.
- [mobile/](app/src/mobile/) — Mobile-only entry and chrome. Compiled by `webpack.mobile.js`.
- [plugin/](app/src/plugin/) — Host-side plugin API runtime (paired with the external [petal](https://github.com/siyuan-note/petal) type package).
- [asset/](app/src/asset/), [assets/](app/src/assets/), [emoji/](app/src/emoji/) — Static assets and asset handling (PDF viewer lives under `src/asset/pdf` and is lint-ignored).
- [types/](app/src/types/) — Ambient `.d.ts` declarations.
- [constants.ts](app/src/constants.ts) — **Single source of truth for versioning**, default URLs, API endpoints. Update here when bumping versions.

## Cross-cutting conventions

- **Lute is authoritative for Markdown.** Any parsing/serialization of `.sy` or markdown content must go through `github.com/88250/lute` on the kernel side or its JS build on the frontend. The pinned version lives in [kernel/go.mod](kernel/go.mod); upgrades come from the upstream siyuan-note/lute bumps and usually land as dedicated commits (see recent history: "⬆️ Upgrade lute").
- **Dejavu is the data repo.** Sync and snapshotting go through `github.com/siyuan-note/dejavu`; do not write backup/sync logic from scratch.
- **Bazaar is the marketplace client.** [kernel/bazaar/](kernel/bazaar/) pulls themes/plugins/icons/widgets/templates from the external `siyuan-note/bazaar` repo; do not fetch these assets ad-hoc.
- **Transactions.** Editor mutations are batched as transactions; [kernel/model/transaction.go](kernel/model/transaction.go) is the canonical write path. New block mutations must go through it to stay consistent with the SQL queue and the dejavu history index.
- **API auth.** Most endpoints require `CheckAuth` and often `CheckAdminRole` + `CheckReadonly`. Add middleware in [kernel/api/router.go](kernel/api/router.go), not ad-hoc inside handlers.
- **i18n.** Language keys live under [app/appearance/langs/](app/appearance/langs/). [scripts/check-lang-keys.py](scripts/check-lang-keys.py) enforces parity across locales — run it after adding/removing keys.
- **Deprecated endpoints** are marked with a `deprecated` middleware and a TODO comment citing the removal date and GitHub issue (e.g. `router.go` has several with a 2026-06-30 sunset). When removing one, search both kernel and frontend for callers.
- **Development mode.** The kernel does **not** auto-start from `pnpm run start` — you must launch `SiYuan-Kernel --mode=dev` first. This is intentional and documented in CONTRIBUTING.
- **PR target branch.** Upstream work is developed on `dev` per CONTRIBUTING, even though this checkout's default is `master`. Confirm with the user which branch they want before pushing.

## When adding features

A typical feature touches: a new route in [kernel/api/router.go](kernel/api/router.go), a handler in `kernel/api/<area>.go`, business logic in `kernel/model/<area>.go`, possibly a SQL migration via [kernel/sql/database.go](kernel/sql/database.go), and a frontend caller in `app/src/...` (usually via `fetchPost` / `fetchSyncPost` helpers). If the feature affects the editor, expect most of the work to be in [app/src/protyle/](app/src/protyle/).

When modifying data-on-disk formats (`.sy` JSON, `storage/`, `av/`), bump the relevant version constant and add a migration — users rely on forward compatibility of existing workspaces.
