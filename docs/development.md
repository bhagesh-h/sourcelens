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

`.github/workflows/ci.yml` runs on every push to `main`: `go vet`, `go test`
and `sourcelens test` for Go, and pytest, `sourcelens test` and ruff for
Python 3.10, 3.12 and 3.13.

## Website

The project page https://bhagesh-h.github.io/sourcelens/ is the single file
`site/index.html`, with the logo and icons beside it. No build step: open the
file in a browser to see changes. `.github/workflows/pages.yml` publishes the
`site/` folder to GitHub Pages on every push to `main` that changes it.

## Releasing

Set the version in `src/sourcelens/__init__.py` and `cmd/sourcelens/main.go`,
then push a tag such as `v1.0.1`. Two workflows run:

- `publish` uploads the Python package to PyPI;
- `release` attaches the Go binaries for Linux and macOS to a GitHub release,
  named `sourcelens_<os>_<arch>`.

[publish.md](../publish.md) has the one-time PyPI setup and the release steps,
including a trial upload to TestPyPI. Its local commands run in a Docker
container (`publish/publish.sh`), so nothing is installed on the machine that
releases.
