GOLANGCI_LINT_VERSION := v2.14.0
GOLANGCI_LINT := bin/golangci-lint-$(GOLANGCI_LINT_VERSION)

.PHONY: build test vet lint tools check

build:
	cd backend && go build -o ../bin/ ./cmd/...

test:
	cd backend && go test ./...

vet:
	cd backend && go vet ./...

lint: $(GOLANGCI_LINT)
	cd backend && ../$(GOLANGCI_LINT) run ./...

tools: $(GOLANGCI_LINT)

$(GOLANGCI_LINT):
	GOBIN=$(abspath bin) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	mv bin/golangci-lint $@

check: vet lint test build
