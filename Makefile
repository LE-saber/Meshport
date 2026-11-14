VERSION ?= $(shell date +%Y.%m.%d)-dev
LDFLAGS := -s -w -X meshport/internal/api.Version=$(VERSION)
FRP_VERSION ?= 0.72.0
BIN := bin

.PHONY: build test race e2e frp-bins openapi console release desktop android-aar android clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/meshportd ./cmd/meshportd
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/meshctl ./cmd/meshctl

test:
	go vet ./...
	go test -count=1 ./...

race:
	go test -race -count=1 ./...

# Builds frps/frpc from the Go module source (the module zip lacks the dashboard
# assets, so a placeholder page is generated) for the end-to-end test.
frp-bins:
	rm -rf .frp-src && go mod download github.com/fatedier/frp@v$(FRP_VERSION)
	cp -r $$(go env GOMODCACHE)/github.com/fatedier/frp@v$(FRP_VERSION) .frp-src && chmod -R u+w .frp-src
	for d in frps frpc; do mkdir -p .frp-src/web/$$d/dist && echo '<html></html>' > .frp-src/web/$$d/dist/index.html; done
	cd .frp-src && go build -o ../$(BIN)/frps ./cmd/frps && go build -o ../$(BIN)/frpc ./cmd/frpc
	rm -rf .frp-src

e2e: frp-bins
	MESHPORT_FRPS=$(CURDIR)/$(BIN)/frps MESHPORT_FRPC=$(CURDIR)/$(BIN)/frpc go test -count=1 -run TestFRPEndToEnd -v ./internal/api/

openapi:
	go run ./cmd/meshportd openapi > docs/openapi.json

# release/meshport-server-<version>-linux-<arch>.tar.gz with bin/ deploy/ docs/
console:
	node --check internal/console/web/app.js
	node --test tests/console_ui.test.cjs

release: console
	@set -eu; for arch in amd64 arm64; do \
	  d=release/meshport-server-$(VERSION)-linux-$$arch; rm -rf $$d; mkdir -p $$d/bin; \
	  GOOS=linux GOARCH=$$arch CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $$d/bin/meshportd ./cmd/meshportd; \
	  GOOS=linux GOARCH=$$arch CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $$d/bin/meshctl ./cmd/meshctl; \
	  cp -r deploy docs README.md $$d/; chmod +x $$d/deploy/install.sh; \
	  tar -C release -czf $$d.tar.gz $$(basename $$d); \
	  (cd release && sha256sum $$(basename $$d).tar.gz >> SHA256SUMS); \
	done
	@ls -la release

# Desktop client (no CGO): Linux and Windows, amd64 and arm64.
DESKTOP_LDFLAGS := -s -w -X main.version=$(VERSION)
DESKTOP_TARGETS ?= linux/amd64 linux/arm64 windows/amd64 windows/arm64
desktop:
	@mkdir -p release
	@set -eu; for t in $(DESKTOP_TARGETS); do \
	  os=$${t%/*}; arch=$${t#*/}; n=meshport-desktop-$(VERSION)-$$os-$$arch; d=release/$$n; mkdir -p $$d; \
	  if [ $$os = windows ]; then \
	    GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build -trimpath -ldflags "$(DESKTOP_LDFLAGS) -H windowsgui" -o $$d/meshport.exe ./cmd/meshport || exit 1; \
	    cp packaging/windows/README.txt $$d/; (cd release && zip -qr $$n.zip $$n); \
	  else \
	    GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build -trimpath -ldflags "$(DESKTOP_LDFLAGS)" -o $$d/meshport ./cmd/meshport || exit 1; \
	    cp packaging/linux/* packaging/icons/* $$d/; tar -C release -czf $$d.tar.gz $$n; \
	  fi; \
	done
	@ls -la release

# Android: gomobile AAR, then the Gradle app (needs Android SDK + NDK, JDK 17, Gradle 8.9+).
android-aar:
	go install golang.org/x/mobile/cmd/gomobile@$$(go list -m -f '{{.Version}}' golang.org/x/mobile) golang.org/x/mobile/cmd/gobind@$$(go list -m -f '{{.Version}}' golang.org/x/mobile)
	gomobile init
	gomobile bind -v -trimpath -ldflags "-s -w -checklinkname=0" -target=android/arm64,android/arm,android/amd64 -androidapi 24 -o android/app/libs/meshcore.aar ./mobile/meshcore

android: android-aar
	cd android && VERSION_NAME=$(VERSION) gradle assembleRelease

clean:
	rm -rf $(BIN) release .frp-src
