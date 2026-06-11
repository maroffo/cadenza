# ABOUTME: Quality gates for cadenza: `make check` (lint, vet, fmt, vuln, test) and `make test-e2e`
# ABOUTME: Go targets activate once go.mod exists; consumed by pre-commit hook, CI, and review preflight

GOLANGCI_LINT ?= golangci-lint
# Fall back to GOPATH/bin so hooks and CI shells without it on PATH still gate.
GOVULNCHECK ?= $(shell command -v govulncheck 2>/dev/null || echo "$$(go env GOPATH)/bin/govulncheck")

# The repo bootstraps docs-first and the gate must stay green at every
# commit, so Go targets are conditional on go.mod existing.
GO_READY := $(wildcard go.mod)

.PHONY: check lint vet fmt-check vuln test test-e2e emulator sh-check purity-check

check: lint vet fmt-check vuln test sh-check purity-check

# Shell scripts get at least a parse check; nothing ships unparseable again.
sh-check:
	@for f in deploy/*.sh; do bash -n "$$f" || exit 1; done
	@echo "sh-check: ok"

# The pure packages (workout, safety, verdict) must never grow I/O or model
# deps: the safety design leans on this boundary (plan, lint-enforced).
purity-check:
	@bad=$$(go list -f '{{ join .Imports "\n" }}' ./internal/workout ./internal/safety ./internal/verdict | sort -u | grep -E 'anthropic|firestore|net/http|cloud\.google' || true); \
	if [ -n "$$bad" ]; then echo "PURITY VIOLATION: $$bad"; exit 1; fi
	@echo "purity-check: ok"

# Firestore emulator via Docker (the gcloud component needs a Java JRE; Docker doesn't).
# Tests pick it up with FIRESTORE_EMULATOR_HOST=localhost:8090.
emulator:
	docker run --rm --name cadenza-firestore-emu -p 8090:8090 \
		gcr.io/google.com/cloudsdktool/google-cloud-cli:emulators \
		gcloud emulators firestore start --host-port=0.0.0.0:8090

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
