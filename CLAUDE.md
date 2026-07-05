# CLAUDE.md

LAN-only personal media server for a Mac mini: one Go binary, SQLite, React
web client, ffmpeg children. **Read `docs/BUILD-PLAN.md` first** — work
proceeds milestone by milestone, and a milestone isn't done until its
acceptance criteria actually pass (run them, don't assume). Current state:
**M9 complete — all milestones M1–M9 done**. M9 added graceful shutdown
(HTTP drain → kill ffmpeg → drain workers → close DB), the launchd agent +
`deploy/install.sh`, loopback `debug.pprof_port`, `scripts/soak.sh`, the
200-file event-storm test, the PWA manifest/icons, and an a11y pass; it also
fixed two load bugs (`_txlock=immediate` for SQLITE_BUSY, migration 0004 for
the uploads→item purge FK). Hardware-only acceptance (reboot, 24 h soak,
Lighthouse, real-device M6/M7 timing) is captured in `deploy/RUNBOOK.md`.

Specs are implementation-grade and are the contract — when code and spec
disagree, the spec wins unless you surface a reason:
`docs/ARCHITECTURE.md` (top-level design), `docs/SPEC-BACKEND.md` (packages,
schema, jobs, playback), `docs/SPEC-API.md` (wire shapes, snake_case),
`docs/SPEC-FRONTEND.md` (routes, data layer), `docs/DESIGN-SYSTEM.md`
(tokens, component standards).

## Commands

```sh
make test                 # everything CI runs: go vet+test, eslint, tsc, vite build
make dev                  # go run ./cmd/server --config config.example.yml
cd web && npm run dev     # Vite dev server, proxies /api → :8484
make build                # vite build + go build -tags embedweb → bin/media-server
```

Run a single Go test: `go test ./internal/store/ -run TestFileLifecycle`.

## Hard rules (from the specs, enforced in review)

- **All SQL lives in `internal/store`** — one file per entity. Handlers and
  services never import `database/sql`.
- **No web framework** (stdlib `net/http` 1.22+ pattern routing), **no new
  dependency without a stated reason**. Current allowlist: modernc sqlite,
  yaml.v3, fsevents, and xxh3.
- Errors over the wire use the envelope `{"error":{"code","message"}}` —
  `code` is a stable slug; see `internal/httpapi/respond.go`.
- Files are identified as `(root_id, rel_path)`, never absolute paths. An
  unmounted volume is a *state* (root/files offline), never an error or a
  deletion. Nothing in the catalog is destroyed by a disk going away.
- Every ffmpeg/ffprobe child: `exec.CommandContext` + hard timeout.
- Frontend components use design tokens only — **no hex colors outside
  `web/src/theme/tokens.css`** (M1 acceptance greps `src/ui/` for `#`;
  keep it passing). One amber accent per screen; sentence case copy.
- Media test fixtures are generated with `ffmpeg -f lavfi` (script, not
  checked-in binaries).

## Layout & non-obvious decisions

- `cmd/server/main.go` wires config → db → store/bus → httpapi. New
  subsystems (watcher, jobs, playback) get their own `internal/` package
  per SPEC-BACKEND's layout and are wired there.
- **Migrations**: numbered `internal/db/migrations/NNNN_*.sql`, embedded,
  tracked via `PRAGMA user_version`. Never edit an applied migration — add
  the next number. Migrate runs on every boot and must stay idempotent.
- **Timestamps** are SQLite text `YYYY-MM-DD HH:MM:SS` UTC; use
  `store.FormatTime`/`store.ParseTime`. Store structs keep them as strings.
- **Web embedding**: `web/embed_{on,off}.go` (package `webdist`) split on
  the `embedweb` build tag, so plain `go build` works without Node/dist.
  Vet both: `go vet -tags embedweb ./...` (make vet does).
- go.mod has `ignore ./web/node_modules` — some npm packages ship `.go`
  files; don't remove it.
- **Event bus** (`internal/events`): publish must never block — subscribers
  get drop-oldest buffers. SSE (M4) fans out from this bus; clients heal by
  refetching, so lossy delivery is by design.
- The HTTP logger middleware's `statusRecorder` passes `Flush` through —
  required for SSE later; keep it if you touch middleware.
- **Tailwind v4 mapping**: raw tokens (DESIGN-SYSTEM names) live in
  unlayered `:root`/`[data-theme]` blocks; `@theme inline` maps utility
  names (`bg-surface`, `text-secondary`, `border-line`). Tailwind emits
  self-referential vars for same-named entries but they're in
  `@layer theme` and always lose to our unlayered blocks — this is deliberate
  and verified; don't "fix" it by renaming design-system tokens.
- `react-router` is pinned `^7` to match SPEC-FRONTEND (npm wants v8).
  Amber button fills use `--color-accent-fill*` (same amber in both themes);
  amber text/borders use `--color-accent` (darkened in light mode).
- Theme is applied pre-paint by an inline script in `web/index.html`;
  `ThemeProvider` owns it afterwards. The player route (M3) must force
  `data-theme="dark"` on its subtree.

## Testing conventions

- Store/db tests: real SQLite in `t.TempDir()` (not `:memory:` — pooling
  gives each conn its own memory db), via `db.Open` + `db.Migrate`.
- httpapi: `httptest` round-trips against a real migrated store.
- Pure logic (title parser, playback decisions): table-driven fixtures —
  the table *is* the spec.
- Watcher/FSEvents tests (M4+): macOS-only, guard with build tags.
