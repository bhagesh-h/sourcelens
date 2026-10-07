# Development

```bash
git clone https://github.com/bhagesh-h/sourcelens.git && cd sourcelens
make python        # pip install -e ".[dev]"
make go            # bin/sourcelens
make test          # go vet, go test, pytest, and sourcelens test for both
make parity        # run parity/cases.txt through both and compare every output
```

Every feature goes into both implementations, with the same flags and output.

## Parity check

`parity/check.sh` runs about 70 commands through both implementations:

- help texts, plans and errors;
- queries and exports of a real catalogue;
- a new topic created and built offline.

It then compares stdout, stderr, exit codes and every file written. A change
to one implementation is finished when the parity check passes.

## Continuous integration

`.github/workflows/ci.yml` runs on every push to `main` and every pull
request, on Linux, macOS (Apple silicon) and Windows: `go vet`, `go test` and
`sourcelens test` for Go; pytest and `sourcelens test` for Python (3.10, 3.12
and 3.13 on Linux, 3.12 on macOS and Windows), and ruff.

File locks (`src/sourcelens/common/locks.py`, `cmd/sourcelens/lock_*.go`) and
the terminal (`term_*.go`) are the only code that differs by platform. Text
files are written as UTF-8 with `\n` line ends everywhere, so a catalogue is
the same on every system.

## Website

The project page https://bhagesh-h.github.io/sourcelens/ is the single file
`site/index.html`, with the logo and icons beside it. No build step: open the
file in a browser to see changes. `.github/workflows/pages.yml` publishes the
`site/` folder to GitHub Pages on every push to `main` that changes it.

## Releasing

Set the version in `src/sourcelens/__init__.py` and `cmd/sourcelens/main.go`,
then push a tag such as `v1.0.2`. Two workflows run:

- `publish` uploads the Python package to PyPI;
- `release` builds the Go binaries, runs each one on its own system (Linux
  x86-64 and ARM64, macOS, Windows), and attaches them to a GitHub release:
  `sourcelens_linux_amd64`, `sourcelens_linux_arm64`, `sourcelens_macos`,
  `sourcelens_windows_amd64.exe`, `sourcelens_windows_arm64.exe` and
  `SHA256SUMS`.

`sourcelens_macos` is one universal file for Apple silicon and Intel, joined
from the two builds by `tools/lipo` (a small Go version of Apple's `lipo
-create`, so `make release` works on any system). The older names
`sourcelens_darwin_arm64` and `sourcelens_darwin_amd64` are copies of it.
`install.sh` and `install.ps1` download the right file for a system.

[publish.md](../publish.md) has the one-time PyPI setup and the release steps,
including a trial upload to TestPyPI. Its local commands run in a Docker
container (`publish/publish.sh`), so nothing is installed on the machine that
releases.
