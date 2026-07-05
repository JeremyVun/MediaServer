# Home Media server

A LAN-only personal media server for a Mac mini with attached storage.
Single Go binary, SQLite, React web client, ffmpeg for media work.

## Requirements

- Go ≥ 1.26 (what `go.mod` declares; newer toolchains auto-download it)
- Node ≥ 20 (frontend build only)
- ffmpeg / ffprobe on `$PATH` (`brew install ffmpeg`)

## Run it

```sh
# Backend (uses config.example.yml; copy to ~/media-server/config.yml to customize)
go run ./cmd/server --config config.example.yml

# Frontend dev server (proxies /api to :8484)
cd web && npm install && npm run dev
```

Open the Vite URL it prints. `/` is the library; `/demo` renders every UI
primitive in both themes. `curl localhost:8484/api/health` shows server
status.

## Production build

```sh
make build        # vite build + go build -tags embedweb → bin/media-server
```

The result is one binary with the web app embedded; point it at a config
with `--config` (default `~/media-server/config.yml`).

## Deploy (Mac mini, launchd)

```sh
deploy/install.sh          # build, install to ~/media-server, load the LaunchAgent
```

This builds the binary, installs it under `~/media-server/bin`, writes a
starter `config.yml` if none exists (edit its `library_roots`, then re-run),
and loads a per-user launchd agent (`com.jeremy.mediaserver`) that starts at
login and restarts on crash. On `SIGTERM`/reboot the server drains
gracefully (stops accepting, kills ffmpeg children, closes the DB) in well
under its 3 s target. `deploy/uninstall.sh` removes the agent.

Operational extras:

- **Health:** `curl localhost:8484/api/health` (version, uptime, roots, free
  space, active sessions, queue depth).
- **Logs:** rotated JSON at `~/media-server/data/logs/server.log`
  (5 × 10 MB); raw stdio at `launchd.{out,err}.log`.
- **Profiling:** set `debug.pprof_port` in the config to expose
  `net/http/pprof` on `127.0.0.1:<port>` (loopback only, never the LAN bind).
- **Soak test:** `scripts/soak.sh` drives upload/play/seek/delete in a loop
  and asserts flat RSS, no goroutine leak, and zero orphaned ffmpeg

## Development

```sh
make test         # go vet + go test + eslint + tsc + vite build (what CI runs)
```

Layout: `cmd/server` wires config → db → services → http; all SQL lives in
`internal/store`; the React app lives in `web/`
