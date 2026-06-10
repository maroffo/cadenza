# ABOUTME: Quality gates for cadenza: `make check` (lint, vet, fmt, vuln, test) and `make test-e2e`
# ABOUTME: Go targets activate once go.mod exists; consumed by pre-commit hook, CI, and review preflight

GOLANGCI_LINT ?= golangci-lint
GOVULNCHECK ?= govulncheck

# The repo bootstraps docs-first and the gate must stay green at every
# commit, so Go targets are conditional on go.mod existing.
GO_READY := $(wildcard go.mod)

.PHONY: check lint vet fmt-check vuln test test-e2e

check: lint vet fmt-check vuln test

ifneq ($(GO_READY),)

lint:
	$(GOLANGCI_LINT) run ./...

vet:
	go vet ./...

fmt-check:
	@test -z "$$(gofmt -l .)" || (echo "gofmt: files need formatting:" && gofmt -l . && exit 1)

vuln:
	$(GOVULNCHECK) ./...

test:
	go test -race -count=1 ./...

test-e2e:
	go test -race -count=1 -tags=e2e ./e2e/...

else

lint vet fmt-check vuln test test-e2e:
	@echo "make $@: no go.mod yet (docs-only stage); Go checks activate at 'go mod init'"

endif
