# Install

sourcelens runs on Linux and macOS. There are two implementations of the same
command, with the same options and output. Install one of them.

## Python package

Python 3.10 or newer:

```bash
pip install sourcelens            # or: pipx install sourcelens
pip install "sourcelens[pdf]"     # adds PyMuPDF for better PDF to Markdown conversion
```

## Go binary

A single static file of about 8 MB, with no dependencies. Every
[release](https://github.com/bhagesh-h/sourcelens/releases/latest) has one per
system:

| system | file |
|---|---|
| Linux, x86-64 | `sourcelens_linux_amd64` |
| Linux, ARM64 | `sourcelens_linux_arm64` |
| macOS, Apple silicon | `sourcelens_darwin_arm64` |
| macOS, Intel | `sourcelens_darwin_amd64` |

Download it, make it executable and put it in a folder on your `PATH`:

```bash
curl -fLo sourcelens https://github.com/bhagesh-h/sourcelens/releases/latest/download/sourcelens_linux_amd64
chmod +x sourcelens
mv sourcelens ~/.local/bin/
```

The binaries are not signed. If macOS refuses to open a file downloaded in a
browser, run `xattr -d com.apple.quarantine sourcelens` once. Files
downloaded with `curl` are not affected.

With Go 1.23 or newer:

```bash
go install github.com/bhagesh-h/sourcelens/cmd/sourcelens@latest
```

## Docker

```bash
docker build -t sourcelens https://github.com/bhagesh-h/sourcelens.git
docker run --rm -v "$HOME/sourcelens:/data" sourcelens "CRISPR base editing"
```

The image holds the Go binary and poppler. Catalogues are written to the
folder mounted at `/data`.

## Optional tools

- `poppler-utils` (`apt install poppler-utils`, `brew install poppler`)
  converts PDFs to text and Markdown. The Go binary needs it for that; the
  Python package uses it when PyMuPDF is not installed.
- `gh`, the GitHub CLI: when it is logged in, sourcelens uses its token for
  faster GitHub searches.

## Python or Go

The two implementations take the same commands and options, write the same
files and print the same text. Use whichever is easier to install. Both
always run their own steps; they share no code.

| | Python | Go |
|---|---|---|
| install | `pip install sourcelens` | a release binary or `go install` |
| source | `src/sourcelens/` | `cmd/sourcelens/` |
| PDF conversion | PyMuPDF if installed, else poppler | poppler |

Known differences:

- Markdown made from PDFs differs in detail between PyMuPDF and poppler.
- Some publisher sites (for example nature.com) answer Go's HTTP client with a
  bot check instead of the PDF. sourcelens does not try to get past such
  checks.
- The keys inside `corpus/records.jsonl.gz` may be in a different order. Both
  read either file.

Both implementations can work on the same catalogue, one after the other.
