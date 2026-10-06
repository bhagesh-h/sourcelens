# Install

sourcelens runs on Linux, macOS and Windows. There are two implementations of
the same command, with the same options and output. Install one of them.

## Python package

Python 3.10 or newer, on any system:

```bash
pip install sourcelens            # or: pipx install sourcelens
pip install "sourcelens[pdf]"     # adds PyMuPDF for better PDF to Markdown conversion
```

## Go binary

A single file of about 8 MB, with no dependencies. The installers pick the
right one for the system and put it on your `PATH`.

Linux and macOS (into `~/.local/bin`):

```bash
curl -fsSL https://raw.githubusercontent.com/bhagesh-h/sourcelens/main/install.sh | sh
```

Windows, in PowerShell (into `%LOCALAPPDATA%\sourcelens`, added to your PATH):

```powershell
irm https://raw.githubusercontent.com/bhagesh-h/sourcelens/main/install.ps1 | iex
```

Or download the file yourself from the
[latest release](https://github.com/bhagesh-h/sourcelens/releases/latest):

| system | file |
|---|---|
| Linux, x86-64 | `sourcelens_linux_amd64` |
| Linux, ARM64 | `sourcelens_linux_arm64` |
| macOS, Apple silicon and Intel | `sourcelens_macos` (one file for both) |
| Windows, x86-64 | `sourcelens_windows_amd64.exe` |
| Windows, ARM64 | `sourcelens_windows_arm64.exe` |

```bash
curl -fLo sourcelens https://github.com/bhagesh-h/sourcelens/releases/latest/download/sourcelens_macos
chmod +x sourcelens
mv sourcelens ~/.local/bin/
```

On Windows, rename the file to `sourcelens.exe` and put it in a folder on
your PATH. `SHA256SUMS` in each release holds the checksums.

macOS:

- `sourcelens_macos` runs natively on Apple silicon and on Intel Macs.
  (Releases up to 1.0.0 had a separate file per processor; the Intel one
  stops with `zsh: bad CPU type in executable` on Apple silicon without
  Rosetta. The `sourcelens_darwin_*` names now hold the same universal file.)
- The binaries are not signed. If macOS refuses to open a file downloaded in
  a browser, run `xattr -d com.apple.quarantine sourcelens` once. Files
  downloaded with `curl` or the installer are not affected.

With Go 1.23 or newer, on any system:

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

- `poppler-utils` (`apt install poppler-utils`, `brew install poppler`; on
  Windows the poppler release for Windows, with its `bin` folder on the PATH)
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
