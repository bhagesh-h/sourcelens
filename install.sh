#!/bin/sh
# Installs the sourcelens binary for this system (Linux or macOS, x86-64 or ARM)
# from the latest GitHub release:
#
#   curl -fsSL https://raw.githubusercontent.com/bhagesh-h/sourcelens/main/install.sh | sh
#
# SOURCELENS_DIR chooses the folder (default ~/.local/bin); SOURCELENS_VERSION
# a release such as v1.1.0 (default the latest).
set -eu

repo="https://github.com/bhagesh-h/sourcelens/releases"
dir="${SOURCELENS_DIR:-$HOME/.local/bin}"
case "$(uname -s)" in
    Linux)
        case "$(uname -m)" in
            x86_64 | amd64) file=sourcelens_linux_amd64 ;;
            aarch64 | arm64) file=sourcelens_linux_arm64 ;;
            *) echo "sourcelens: no binary for $(uname -m); use: pip install sourcelens" >&2; exit 1 ;;
        esac ;;
    Darwin) file=sourcelens_macos ;;  # one file for Apple silicon and Intel
    *) echo "sourcelens: on Windows run install.ps1 (see the README)" >&2; exit 1 ;;
esac
if [ -n "${SOURCELENS_VERSION:-}" ]; then
    url="$repo/download/$SOURCELENS_VERSION/$file"
else
    url="$repo/latest/download/$file"
fi

mkdir -p "$dir"
tmp="$dir/.sourcelens.download"
echo "downloading $url"
curl -fL --progress-bar -o "$tmp" "$url"
chmod +x "$tmp"
mv "$tmp" "$dir/sourcelens"
"$dir/sourcelens" version
case ":$PATH:" in
    *":$dir:"*) ;;
    *) echo "add $dir to your PATH, e.g.: echo 'export PATH=\"$dir:\$PATH\"' >> ~/.profile" ;;
esac
