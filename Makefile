GO ?= go

.PHONY: build arm64 fmt check vulnerabilities

build:
	cd backend && CGO_ENABLED=0 $(GO) build -trimpath -ldflags='-s -w' -o bin/sharefile ./cmd/sharefile

arm64:
	cd backend && GOOS=linux GOARCH=arm64 GOARM64=v8.0 CGO_ENABLED=0 $(GO) build -trimpath -ldflags='-s -w' -o bin/sharefile-linux-arm64 ./cmd/sharefile

fmt:
	cd backend && $(GO) fmt ./...

check:
	cd backend && $(GO) vet ./...
	cd backend && $(GO) test -race ./...

vulnerabilities:
	cd backend && $(GO) run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...
