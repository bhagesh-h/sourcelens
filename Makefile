# sourcelens: build, test and package both implementations.
#
#   make go          build bin/sourcelens (needs Go 1.23+)
#   make python      install the Python package in editable mode (needs Python 3.10+)
#   make test        unit tests of both, plus the append-only check
#   make parity      compare the two implementations (needs both installed)
#   make release     Go binaries for Linux and macOS in dist/ (sourcelens_<os>_<arch>)
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
	for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do \
		os=$${target%/*}; arch=$${target#*/}; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags="$(LDFLAGS)" \
			-o dist/sourcelens_$${os}_$${arch} ./cmd/sourcelens; \
	done

dist:
	rm -rf dist
	$(PYTHON) -m build --outdir dist .
	$(PYTHON) -m twine check --strict dist/*

docker:
	docker build --build-arg VERSION=$(VERSION) -t sourcelens:$(VERSION) .

clean:
	rm -rf bin dist build .pytest_cache src/*.egg-info
