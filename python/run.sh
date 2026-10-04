#!/usr/bin/env bash
# Run a command inside the Python implementation's container.
#
#   python/run.sh build                                   # build the image
#   python/run.sh python python/pullliturature/search_europepmc.py
#   python/run.sh bash                                    # shell in the image
#
# Reads config/local.yaml (git-ignored; template config/local.template.yaml):
# mounts the workspace at /work, the output folder (research_dir, or
# $LITSEARCH_RESEARCH) at /research and the reference projects
# (reference_repos) read-only under /refs, and passes contact_email and the
# API credentials as environment variables. The configuration the steps read
# is <research_dir>/config/litsearch.yaml, created from
# config/litsearch.template.yaml when missing.
set -euo pipefail

IMAGE="${LITSEARCH_PY_IMAGE:-litsearch-python:1.0}"
HERE="$(cd "$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"

if [[ "${1:-}" == "build" ]]; then
  exec docker build -t "$IMAGE" -f "$HERE/Dockerfile" "$HERE"
fi
if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
  docker build -t "$IMAGE" -f "$HERE/Dockerfile" "$HERE" >&2
fi

# local settings: paths on this machine and credentials (git-ignored;
# template config/local.template.yaml)
LOCAL="${LITSEARCH_LOCAL:-$ROOT/config/local.yaml}"
if [[ ! -f "$LOCAL" ]]; then
  echo "litSearch: $LOCAL not found; copy config/local.template.yaml to config/local.yaml and fill it in" >&2
  exit 2
fi
# yget KEY: the value of a top-level "KEY: value" line (quotes and trailing comment dropped)
yget() {
  sed -n "s/^$1:[[:space:]]*//p" "$LOCAL" | head -1 |
    sed -e 's/[[:space:]]#.*$//' -e 's/[[:space:]]*$//' -e 's/^"\(.*\)"$/\1/' -e "s/^'\(.*\)'$/\1/"
}
RESEARCH="${LITSEARCH_RESEARCH:-$(yget research_dir)}"
if [[ -z "$RESEARCH" || "$RESEARCH" == /path/to/* ]]; then
  echo "litSearch: set research_dir in $LOCAL" >&2
  exit 2
fi
mkdir -p "$RESEARCH/logs" "$RESEARCH/config"
# the live configuration is kept (and synced) with the outputs; a new output
# folder starts from the template
if [[ ! -f "$RESEARCH/config/litsearch.yaml" ]]; then
  cp "$ROOT/config/litsearch.template.yaml" "$RESEARCH/config/litsearch.yaml"
  echo "litSearch: created $RESEARCH/config/litsearch.yaml from config/litsearch.template.yaml" >&2
fi
REFS=()
while read -r r; do
  [[ -n "$r" && -d "$r" ]] && REFS+=(-v "$r:/refs/$(basename "$r"):ro")
done < <(sed -n '/^reference_repos:/,/^[^ ]/{s/^[[:space:]]*-[[:space:]]*//p}' "$LOCAL" | sed -e 's/[[:space:]]#.*$//' -e 's/[[:space:]]*$//')

# credentials: environment first, then local.yaml; the GitHub token falls back
# to the gh CLI. They reach the container as environment variables passed by
# name (-e NAME), so they appear neither in files nor on the command line.
export CONTACT_EMAIL="${CONTACT_EMAIL:-$(yget contact_email)}"
[[ "$CONTACT_EMAIL" == "you@example.org" ]] && CONTACT_EMAIL=""
export GITHUB_TOKEN="${GITHUB_TOKEN:-$(yget github_token)}"
if [[ -z "$GITHUB_TOKEN" ]] && command -v gh >/dev/null 2>&1; then
  GITHUB_TOKEN="$(gh auth token 2>/dev/null || true)"
fi
export OPENALEX_API_KEY="${OPENALEX_API_KEY:-$(yget openalex_api_key)}"
export NCBI_API_KEY="${NCBI_API_KEY:-$(yget ncbi_api_key)}"
export AGING_SEARCH_START="${AGING_SEARCH_START:-}" AGING_SEARCH_END="${AGING_SEARCH_END:-}"

# -i always, so piped input (heredocs, python -) reaches the container;
# -t only when attached to a terminal
TTY=(-i)
[[ -t 0 && -t 1 ]] && TTY=(-it)

# label each container with the script it runs, so one step can be found or
# stopped reliably:  docker ps --filter label=litsearch.step=fetch_fulltext.py
STEP="shell"
for a in "$@"; do [[ "$a" == *.py ]] && { STEP="$(basename "$a")"; break; }; done

exec docker run --rm "${TTY[@]}" \
  --label "litsearch.step=$STEP" --label "litsearch.impl=python" \
  --user "$(id -u):$(id -g)" \
  -e HOME=/tmp \
  -e GITHUB_TOKEN -e CONTACT_EMAIL -e OPENALEX_API_KEY -e NCBI_API_KEY \
  -e AGING_SEARCH_START -e AGING_SEARCH_END \
  -e AGING_RESEARCH=/research -e LITSEARCH_CONFIG=/research/config/litsearch.yaml \
  -e LITSEARCH_IMPL=python \
  -v "$ROOT:/work" \
  -v "$RESEARCH:/research" \
  "${REFS[@]}" \
  -w /work \
  "$IMAGE" "$@"
