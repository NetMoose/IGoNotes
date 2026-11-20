.PHONY: clipper-deps clipper clipper-verify clipper-browser clipper-package all ui ui-deps ui-test go test test-git test-race vet verify clean

APP_NAME := igonotes
BUILD_DIR := builds
GIT_TEST_PACKAGES := ./internal/git ./internal/service ./internal/handlers ./cmd/api

all: go

ui-deps:
	npm --prefix web ci

ui: ui-deps
	npm --prefix web run build

ui-test: ui-deps
	npm --prefix web run test:ci

go: ui
	mkdir -p $(BUILD_DIR)
	go build -trimpath -o $(BUILD_DIR)/$(APP_NAME) ./cmd/api

test: ui
	go test ./... -count=1

test-git: ui
	IGONOTES_REQUIRE_GIT_INTEGRATION=1 go test $(GIT_TEST_PACKAGES) -count=1

test-race: ui
	go test -race ./... -count=1

vet: ui
	go vet ./...

verify: ui-test ui
	go test ./... -count=1
	go test -race ./... -count=1
	go vet ./...

clean:
	rm -rf $(BUILD_DIR) web/dist

# Standalone extension; it is never embedded in the Go binary. Python 3 packages ZIPs.
clipper-deps:
	npm --prefix extensions/webclipper ci

clipper: clipper-deps
	npm --prefix extensions/webclipper run build

clipper-verify: clipper-deps
	npm --prefix extensions/webclipper run verify

clipper-browser: ui clipper-deps
	npm --prefix extensions/webclipper run test:browser

clipper-package: clipper
	python3 extensions/webclipper/scripts/package.py
