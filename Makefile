VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
PLATFORMS := linux/arm64 linux/arm linux/amd64
BINARIES := doxaboard-agent doxaboard-server doxaboard-connector

.PHONY: build test lint run-server connector-check

build:
	@mkdir -p dist
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; suffix=$$os-$$arch; \
		[ "$$arch" = arm ] && suffix=$$os-armv7; \
		echo "building $$suffix"; \
		for b in $(BINARIES); do \
			GOOS=$$os GOARCH=$$arch GOARM=7 CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" \
				-o dist/$$b-$$suffix ./cmd/$$b || exit 1; \
		done; \
	done

test:
	go test -race ./...

lint:
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)
	go vet ./...

# Run exactly what CI runs for one connector: make connector-check NAME=rss
connector-check:
	@test -n "$(NAME)" || (echo "usage: make connector-check NAME=<connector>" && exit 2)
	@test -z "$$(gofmt -l connectors/$(NAME))" || (gofmt -l connectors/$(NAME) && exit 1)
	go vet ./connectors/$(NAME)/...
	go run ./cmd/doxaboard-connector lint connectors/$(NAME)
	go test -race -count=1 ./connectors/$(NAME)/...

# Local dev server with throwaway tokens.
run-server:
	DOXABOARD_ADMIN_TOKEN=dev-admin-token-123 DOXABOARD_ENROLLMENT_TOKEN=dev-enroll-token-123 \
		go run ./cmd/doxaboard-server -db dev.db -photos ./dev-photos -debug
