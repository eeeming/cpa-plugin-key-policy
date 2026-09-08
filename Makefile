PLUGIN := cpa-key-quota
PKG := ./cmd/cpa-key-policy
DIST := dist
WEB := web
EMBED_INDEX := internal/plugin/web/dist/index.html

.PHONY: test web-build build-linux-amd64 build-linux-arm64 build-linux clean e2e-docker

test:
	go test ./...

# Build the single-file web UI and place it where the Go embed expects it.
web-build:
	cd $(WEB) && npm install && VITE_HOSTED=1 npm run build
	cp $(WEB)/dist/index.html $(EMBED_INDEX)

build-linux-amd64: web-build
	mkdir -p $(DIST)
	GOOS=linux GOARCH=amd64 CGO_ENABLED=1 go build -buildvcs=false -tags cshared -buildmode=c-shared -o $(DIST)/$(PLUGIN)_linux_amd64.so $(PKG)

build-linux-arm64: web-build
	mkdir -p $(DIST)
	GOOS=linux GOARCH=arm64 CGO_ENABLED=1 go build -buildvcs=false -tags cshared -buildmode=c-shared -o $(DIST)/$(PLUGIN)_linux_arm64.so $(PKG)

build-linux: build-linux-amd64 build-linux-arm64

clean:
	rm -rf $(DIST)

# Full-stack Docker E2E: build linux .so, run CPA + mock Plus/LLM, hit real HTTP paths.
e2e-docker:
	bash e2e/run.sh

# CPA + CPA-Manager-Plus (price table) + plugin .so.
e2e-plus:
	bash e2e/plus/run.sh
