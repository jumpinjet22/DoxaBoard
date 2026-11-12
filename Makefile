VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
PLATFORMS := linux/arm64 linux/arm linux/amd64

.PHONY: build test lint run-server

build:
	@mkdir -p dist
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; suffix=$$os-$$arch; \
		[ "$$arch" = arm ] && suffix=$$os-armv7; \
		echo "building $$suffix"; \
		GOOS=$$os GOARCH=$$arch GOARM=7 CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" \
			-o dist/doxacast-agent-$$suffix ./cmd/doxacast-agent || exit 1; \
		GOOS=$$os GOARCH=$$arch GOARM=7 CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" \
			-o dist/doxacast-server-$$suffix ./cmd/doxacast-server || exit 1; \
	done

test:
	go test -race ./...

lint:
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)
	go vet ./...

# Local dev server with throwaway tokens.
run-server:
	DOXACAST_ADMIN_TOKEN=dev-admin-token-123 DOXACAST_ENROLLMENT_TOKEN=dev-enroll-token-123 \
		go run ./cmd/doxacast-server -db dev.db -photos ./dev-photos -debug
