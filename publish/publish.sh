#!/usr/bin/env bash
# Release commands for the Python package. Each one runs in a Docker container
# (publish/Dockerfile) with the repository mounted at /src, so nothing is
# installed on this machine. Needs Docker and git. See publish.md.
#
#   publish/publish.sh check              build dist/, check it and test the wheel, as the publish workflow does
#   publish/publish.sh testpypi [VERSION] install sourcelens from TestPyPI in a clean container and test it
#   publish/publish.sh pypi [VERSION]     install sourcelens from PyPI in a clean container and test it
#   publish/publish.sh upload REPOSITORY  upload dist/ to testpypi or pypi with an API token (fallback only)
#   publish/publish.sh shell              a shell in the container
#
# PYTHON_VERSION selects the Python in the container (default 3.12).
set -euo pipefail

usage() { sed -n '2,12s/^# \{0,1\}//p' "$0"; exit 2; }
die() { echo "publish: $*" >&2; exit 1; }

if [ "${SOURCELENS_PUBLISH_CONTAINER:-}" != 1 ]; then
    case "${1:-}" in check|testpypi|pypi|upload|shell) ;; *) usage ;; esac
    root=$(cd "$(dirname "$0")/.." && pwd)
    py=${PYTHON_VERSION:-3.12}
    image=sourcelens-publish:py$py
    echo "publish: preparing the container image $image" >&2
    out=$(docker build --quiet --build-arg PYTHON_VERSION="$py" -t "$image" "$root/publish" 2>&1) || { echo "$out" >&2; exit 1; }
    tag=$(git -C "$root" describe --exact-match --tags --match 'v*' HEAD 2>/dev/null || true)
    tty=()
    if [ -t 0 ] && [ -t 1 ]; then tty=(-it); fi
    # TWINE_USERNAME and TWINE_PASSWORD are passed on only when set; twine asks otherwise.
    exec docker run --rm ${tty[@]+"${tty[@]}"} -u "$(id -u):$(id -g)" \
        -e SOURCELENS_PUBLISH_CONTAINER=1 -e TAG="$tag" -e TWINE_USERNAME -e TWINE_PASSWORD \
        -v "$root:/src" -w /src "$image" publish/publish.sh "$@"
fi

# From here on: inside the container.

check_versions() {
    local py go
    py=$(sed -n 's/^__version__ = "\(.*\)"/\1/p' src/sourcelens/__init__.py)
    go=$(sed -n 's/^var version = "\(.*\)"/\1/p' cmd/sourcelens/main.go)
    echo "version: Python package $py, Go $go${TAG:+, tag $TAG}"
    [ "$py" = "$go" ] || die "src/sourcelens/__init__.py ($py) and cmd/sourcelens/main.go ($go) differ"
    [ -z "$TAG" ] || [ "$TAG" = "v$py" ] || die "tag $TAG does not match version $py"
}

pip_try() { /tmp/try/bin/pip install --quiet "$@"; }

# Runs the sourcelens installed in the fresh environment /tmp/try.
run_installed() {
    /tmp/try/bin/sourcelens version
    /tmp/try/bin/sourcelens test
}

case "${1:-}" in
check)
    check_versions
    rm -rf dist
    python -m build --outdir dist .
    twine check --strict dist/*
    python -m venv /tmp/try
    pip_try dist/*.whl pytest
    run_installed
    /tmp/try/bin/python -m pytest -q -p no:cacheprovider tests
    ruff check --no-cache src tests
    ls -l dist
    ;;
testpypi)
    # sourcelens only from TestPyPI; its dependencies are not there, so they come from PyPI.
    python -m venv /tmp/try
    pip_try --no-deps --index-url https://test.pypi.org/simple/ "sourcelens${2:+==$2}"
    pip_try "sourcelens==$(/tmp/try/bin/python -c 'import sourcelens; print(sourcelens.__version__)')"
    run_installed
    ;;
pypi)
    python -m venv /tmp/try
    pip_try "sourcelens${2:+==$2}"
    run_installed
    ;;
upload)
    case "${2:-}" in testpypi|pypi) ;; *) die "usage: publish/publish.sh upload testpypi|pypi" ;; esac
    compgen -G "dist/*.whl" >/dev/null || die "dist/ has no wheel: run publish/publish.sh check first"
    check_versions
    twine check --strict dist/*
    TWINE_USERNAME=${TWINE_USERNAME:-__token__} twine upload --repository "$2" dist/*
    ;;
shell)
    exec bash
    ;;
*)
    usage
    ;;
esac
