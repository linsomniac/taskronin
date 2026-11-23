MODULE      := $(shell go list -m)
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      := $(shell git rev-parse --short HEAD 2>/dev/null)
LDFLAGS     := -s -w -X $(MODULE)/internal/buildinfo.Version=$(VERSION) -X $(MODULE)/internal/buildinfo.Commit=$(COMMIT)
GOVULNCHECK := golang.org/x/vuln/cmd/govulncheck@v1.9.0
FUZZTIME    ?= 30s
PREFIX      ?= $(HOME)/.local

.PHONY: build install test fmt fmt-check vet lint vuln mod-verify assets-verify fuzz check rename-module
.PHONY: release-assets third-party third-party-check snapshot package-smoke

build:         ; CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/tkr ./cmd/tkr
install:       build ; install -D -m 0755 bin/tkr $(PREFIX)/bin/tkr
fmt:           ; golangci-lint fmt ./...
fmt-check:     ; @out=$$(git ls-files -z '*.go' | xargs -0 -r gofmt -l); test -z "$$out" || { echo "$$out"; exit 1; }
vet:           ; go vet ./...
lint:          ; golangci-lint run ./...
test:          ; go test -race -count=1 -timeout=30m ./...
vuln:          ; go run $(GOVULNCHECK) ./...
mod-verify:    ; go mod verify
assets-verify: ; @if [ -f internal/web/static/SHA256SUMS ]; then cd internal/web/static && sha256sum -c SHA256SUMS; fi
fuzz:          ; @scripts/fuzz.sh $(FUZZTIME)   # runs each §13.3 target for FUZZTIME
check: fmt-check vet lint test vuln mod-verify assets-verify third-party-check

release-assets:
	./scripts/release-assets.sh

third-party: release-assets
	cp build/THIRD_PARTY_LICENSES.txt THIRD_PARTY_LICENSES.txt

third-party-check: release-assets
	diff -u THIRD_PARTY_LICENSES.txt build/THIRD_PARTY_LICENSES.txt

snapshot:
	goreleaser release --snapshot --clean

package-smoke:
	docker run --rm -v "$(CURDIR)/dist:/dist:ro" \
		-v "$(CURDIR)/scripts/package-smoke.sh:/package-smoke.sh:ro" \
		debian:stable sh /package-smoke.sh
rename-module: ; @test -n "$(NEW)" && go mod edit -module $(NEW) && \
	grep -rl --include='*.go' '"$(MODULE)/' . | xargs sed -i 's#"$(MODULE)/#"$(NEW)/#g' && \
	sed -i 's#- $(MODULE)$$#- $(NEW)#' .golangci.yml
