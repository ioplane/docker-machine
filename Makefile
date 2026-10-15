.DEFAULT_GOAL := build
PODMAN ?= podman
DEV_IMAGE ?= localhost/ioplane-nutanix-driver:dev
LINT_IMAGE ?= localhost/ioplane-nutanix-driver:lint
RELEASE_IMAGE ?= localhost/ioplane-nutanix-driver:release
SOURCE_REVISION := $(shell git rev-parse HEAD)
BUILD_CREATED := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
GIT_COMMON_DIR := $(shell git rev-parse --path-format=absolute --git-common-dir)
GIT_DIR := $(shell git rev-parse --absolute-git-dir)
PODMAN_RUN = $(PODMAN) run --rm --userns=keep-id:uid=0,gid=0 \
	-v "$(CURDIR):/src:z" -w /src \
	-v ioplane-nutanix-driver-go-modules:/cache/modules \
	-v ioplane-nutanix-driver-go-build:/cache/build
DEV_RUN = $(PODMAN_RUN) $(DEV_IMAGE)
RELEASE_RUN = $(PODMAN_RUN) -v "$(GIT_COMMON_DIR):$(GIT_COMMON_DIR):ro,z" \
	--env GIT_DIR=$(GIT_DIR) --env GIT_WORK_TREE=/src
.PHONY: dev-build lint-build release-build build local test race vet lint fmt-check vuln tidy mod-check deps release-check release check

dev-build:
	$(PODMAN) build --target dev --build-arg SOURCE_REVISION=$(SOURCE_REVISION) \
		--build-arg BUILD_CREATED=$(BUILD_CREATED) -t $(DEV_IMAGE) -f Containerfile.dev .

lint-build:
	$(PODMAN) build --target lint --build-arg SOURCE_REVISION=$(SOURCE_REVISION) \
		--build-arg BUILD_CREATED=$(BUILD_CREATED) -t $(LINT_IMAGE) -f Containerfile.dev .

release-build:
	$(PODMAN) build --target release --build-arg SOURCE_REVISION=$(SOURCE_REVISION) \
		--build-arg BUILD_CREATED=$(BUILD_CREATED) -t $(RELEASE_IMAGE) -f Containerfile.dev .

build local:
	$(DEV_RUN) env CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o build/docker-machine-driver-nutanix ./machine

test:
	$(DEV_RUN) go test -count=1 -shuffle=on -timeout=5m ./...

race:
	$(DEV_RUN) go test -race -count=1 -shuffle=on -covermode=atomic -timeout=10m ./...

vet:
	$(DEV_RUN) go vet ./...

lint:
	$(PODMAN_RUN) $(LINT_IMAGE) golangci-lint run --timeout=5m ./...

fmt-check:
	$(PODMAN_RUN) $(LINT_IMAGE) sh -ec 'golangci-lint fmt --diff > /tmp/format.patch; if test -s /tmp/format.patch; then cat /tmp/format.patch; exit 1; fi'

vuln:
	$(DEV_RUN) go tool govulncheck ./...

tidy:
	$(DEV_RUN) env GOFLAGS=-buildvcs=false go mod tidy

mod-check:
	$(DEV_RUN) sh -ec 'cp go.mod /tmp/go.mod; cp go.sum /tmp/go.sum; GOFLAGS=-buildvcs=false go mod tidy; cmp /tmp/go.mod go.mod; cmp /tmp/go.sum go.sum; go mod verify'

deps:
	$(DEV_RUN) go list -m -u all

release-check:
	$(RELEASE_RUN) $(RELEASE_IMAGE) sh -ec 'cd machine; goreleaser check'

# Called only by the tag release workflow after all quality gates pass.
release:
	$(RELEASE_RUN) --env GITHUB_TOKEN $(RELEASE_IMAGE) sh -ec 'cd machine; goreleaser release --clean'

check: mod-check test race vet lint fmt-check vuln build
