# sourcelens: build, test and package both implementations.
#
#   make go          build bin/sourcelens (needs Go 1.23+)
#   make python      install the Python package in editable mode (needs Python 3.10+)
#   make test        unit tests of both, plus the append-only check
#   make parity      compare the two implementations (needs both installed)
#   make release     Go binaries in dist/: sourcelens_linux_<arch>, sourcelens_windows_<arch>.exe,
#                    sourcelens_macos (one file for Apple silicon and Intel), SHA256SUMS
#   make dist        Python wheel and sdist in dist/, checked with twine (see publish.md)
#   make docker      container image

VERSION := $(shell sed -n 's/^__version__ = "\(.*\)"/\1/p' src/sourcelens/__init__.py)
LDFLAGS := -s -w -X main.version=$(VERSION)
PYTHON ?= python3

.PHONY: go python test test-go test-python parity release dist docker clean

go:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/sourcelens ./cmd/sourcelens

python:
	$(PYTHON) -m pip install -e ".[dev]" build twine

test: test-go test-python

test-go: go
	go vet ./...
	go test ./...
	bin/sourcelens test

test-python:
	$(PYTHON) -m pytest -q
	$(PYTHON) -m sourcelens test

parity: go
	SOURCELENS_GO=bin/sourcelens parity/check.sh

release:
	@mkdir -p dist
	for target in linux/amd64 linux/arm64 windows/amd64 windows/arm64 darwin/amd64 darwin/arm64; do \
		os=$${target%/*}; arch=$${target#*/}; ext=""; [ $$os = windows ] && ext=.exe; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags="$(LDFLAGS)" \
			-o dist/sourcelens_$${os}_$${arch}$$ext ./cmd/sourcelens || exit 1; \
	done
	# one macOS file that runs natively on Apple silicon and Intel; the darwin
	# names stay as copies of it, so every macOS download runs
	go run ./tools/lipo dist/sourcelens_macos dist/sourcelens_darwin_amd64 dist/sourcelens_darwin_arm64
	cp dist/sourcelens_macos dist/sourcelens_darwin_amd64
	cp dist/sourcelens_macos dist/sourcelens_darwin_arm64
	cd dist && (sha256sum sourcelens_* 2>/dev/null || shasum -a 256 sourcelens_*) > SHA256SUMS

dist:
	rm -rf dist
	$(PYTHON) -m build --outdir dist .
	$(PYTHON) -m twine check --strict dist/*

docker:
	docker build --build-arg VERSION=$(VERSION) -t sourcelens:$(VERSION) .

clean:
	rm -rf bin dist build .pytest_cache src/*.egg-info
