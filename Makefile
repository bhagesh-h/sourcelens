# litsearch: build, test and package both implementations.
#
#   make go          build bin/litsearch (needs Go 1.23+)
#   make python      install the Python package in editable mode (needs Python 3.10+)
#   make test        unit tests of both, plus the append-only check
#   make parity      compare the two implementations (needs both installed)
#   make release     Go binaries for Linux and macOS in dist/
#   make dist        Python wheel and sdist in dist/
#   make docker      container image

VERSION := $(shell sed -n 's/^__version__ = "\(.*\)"/\1/p' src/litsearch/__init__.py)
LDFLAGS := -s -w -X main.version=$(VERSION)
PYTHON ?= python3

.PHONY: go python test test-go test-python parity release dist docker clean

go:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/litsearch ./cmd/litsearch

python:
	$(PYTHON) -m pip install -e ".[dev]"

test: test-go test-python

test-go: go
	go vet ./...
	go test ./...
	bin/litsearch test

test-python:
	$(PYTHON) -m pytest -q
	$(PYTHON) -m litsearch test

parity: go
	LITSEARCH_GO=bin/litsearch parity/check.sh

release:
	@mkdir -p dist
	for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do \
		os=$${target%/*}; arch=$${target#*/}; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags="$(LDFLAGS)" \
			-o dist/litsearch_$(VERSION)_$${os}_$${arch} ./cmd/litsearch; \
	done

dist:
	$(PYTHON) -m build --outdir dist .

docker:
	docker build --build-arg VERSION=$(VERSION) -t litsearch:$(VERSION) .

clean:
	rm -rf bin dist build .pytest_cache src/*.egg-info
