.PHONY: build test vet check

build:
	cd backend && go build -o ../bin/ ./cmd/...

test:
	cd backend && go test ./...

vet:
	cd backend && go vet ./...

check: vet test build
