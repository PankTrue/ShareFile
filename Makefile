GO ?= go
NPM ?= npm
PYTHON ?= python3
DOTNET ?= dotnet

.PHONY: build arm64 router-release fmt check vulnerabilities web-build web-check browser-check desktop-check macos-build macos-check desktop-smoke windows-check windows-compile windows-smoke

build:
	cd backend && CGO_ENABLED=0 $(GO) build -trimpath -ldflags='-s -w' -o bin/sharefile ./cmd/sharefile

arm64:
	cd backend && GOOS=linux GOARCH=arm64 GOARM64=v8.0 CGO_ENABLED=0 $(GO) build -trimpath -ldflags='-s -w' -o bin/sharefile-linux-arm64 ./cmd/sharefile

router-release: arm64 web-build
	$(PYTHON) scripts/package-router.py

fmt:
	cd backend && $(GO) fmt ./...

check:
	cd backend && $(GO) vet ./...
	cd backend && $(GO) test -race ./...

vulnerabilities:
	cd backend && $(GO) run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...

web-build:
	cd web && $(NPM) ci
	cd web && $(NPM) run build

web-check:
	cd web && $(NPM) run check

browser-check: build
	cd web && $(NPM) run build
	cd web && $(NPM) run test:browser

desktop-check:
	cd clients/core && $(GO) vet ./...
	cd clients/core && $(GO) test -race ./...

macos-build:
	GO='$(GO)' bash clients/macos/build.sh

macos-check:
	codesign --verify --deep --strict clients/macos/build/ShareFile.app
	clients/macos/build/ShareFile.app/Contents/MacOS/ShareFile --self-test

desktop-smoke: build
	python3 scripts/desktop-smoke.py

windows-check:
	cd clients/windows && $(DOTNET) test --project tests/ShareFile.Core.Tests/ShareFile.Core.Tests.csproj

windows-compile:
	cd clients/windows && $(DOTNET) build ShareFile.slnx -c Release

windows-smoke: build
	cd clients/windows && SHAREFILE_BACKEND='$(CURDIR)/backend/bin/sharefile' $(DOTNET) test --project tests/ShareFile.Core.Tests/ShareFile.Core.Tests.csproj
