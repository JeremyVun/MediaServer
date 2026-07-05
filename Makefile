VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: build server web dev test vet lint typecheck check clean deploy

## build: production single binary (web app embedded)
build: web
	go build -tags embedweb -ldflags "$(LDFLAGS)" -o bin/media-server ./cmd/server

## server: backend-only binary (no embedded web app; use the Vite dev server)
server:
	go build -ldflags "$(LDFLAGS)" -o bin/media-server ./cmd/server

## web: build the frontend to web/dist
web:
	cd web && npm run build

## dev: run the Go server; pair with `cd web && npm run dev` for the UI
dev:
	go run ./cmd/server --config config.example.yml

## test: everything CI runs
test: vet lint typecheck
	go test ./...
	cd web && npm run test
	cd web && npm run build

vet:
	go vet ./...
	go vet -tags embedweb ./...

lint:
	cd web && npm run lint

typecheck:
	cd web && npm run typecheck

clean:
	rm -rf bin web/dist

## deploy: build and (re)install the launchd agent — safe update path, keeps config.yml
deploy:
	deploy/install.sh
