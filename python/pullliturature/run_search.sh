#!/usr/bin/env bash
# Run all literature searches in order (host side; each step runs in Docker).
# PubMed first: its efetch is fast and reliable, so Europe PMC is left with
# identifier lists plus the preprints and non-MEDLINE records only it holds.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"
SCOPE="${1:-all}"
SCOPES=(focused broad)
[[ "$SCOPE" != all ]] && SCOPES=("$SCOPE")
# PubMed for every scope first, so a Europe PMC outage cannot hold it up
for scope in "${SCOPES[@]}"; do
  python/run.sh python python/pullliturature/search_pubmed.py --scope "$scope"
done
for scope in "${SCOPES[@]}"; do
  python/run.sh python python/pullliturature/search_europepmc.py --scope "$scope"
done
# arXiv (focused terms only): imaging / ML clocks posted outside q-bio
if [[ "$SCOPE" != broad ]]; then
  python/run.sh python python/pullliturature/search_arxiv.py
fi
