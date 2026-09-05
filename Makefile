.PHONY: build test lint

build:
	CGO_ENABLED=0 go build ./...

test:
	go test ./...

lint:
	gofmt -l .
	go vet ./...
	@if which golangci-lint > /dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not installed; skipping lint"; \
	fi
