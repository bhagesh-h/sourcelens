#!/usr/bin/env bash
# One command to bring the output folder up to date. Safe to re-run at any time:
# every stage is incremental, and progress.csv only ever gains rows
# (new resources are appended with today's `added_on`; nothing is deleted and
# the hand-edited `notes` / `user_tags` columns are never overwritten).
#
#   python/pipeline/update_all.sh                 # everything
#   python/pipeline/update_all.sh --no-broad      # skip the broad aging/frailty harvest
#   python/pipeline/update_all.sh --no-fulltext   # metadata only
#   FULLTEXT_TIERS=landmark,core FULLTEXT_WORKERS=16 python/pipeline/update_all.sh
#
# Every step runs inside the Python implementation's image via python/run.sh.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"
RUN=python/run.sh
# outputs and logs live in the folder named in config/local.yaml (research_dir)
[[ -f config/local.yaml ]] || { echo "config/local.yaml not found; copy config/local.template.yaml and fill it in" >&2; exit 2; }
RESEARCH="${LITSEARCH_RESEARCH:-$(sed -n 's/^research_dir:[[:space:]]*//p' config/local.yaml | head -1 | sed -e 's/[[:space:]]#.*$//' -e 's/^"\(.*\)"$/\1/')}"
LOG="$RESEARCH/logs"
mkdir -p "$LOG"

# one update at a time: the litSearch CLI (Go and Python) takes the same lock
exec 9>>"$RESEARCH/.pipeline.lock"
if ! flock -n 9; then
  echo "another update is running ($(cat "$RESEARCH/.pipeline.lock" 2>/dev/null)); try again later" >&2
  exit 2
fi
echo "pid $$ since $(date -Is) (update_all.sh)" > "$RESEARCH/.pipeline.lock"
STAMP="$(date +%F_%H%M)"

BROAD=1
FULLTEXT=1
for a in "$@"; do
  case "$a" in
    --no-broad) BROAD=0 ;;
    --no-fulltext) FULLTEXT=0 ;;
    *) echo "unknown option $a" >&2; exit 2 ;;
  esac
done
TIERS="${FULLTEXT_TIERS:-landmark,core,related}"
WORKERS="${FULLTEXT_WORKERS:-12}"

step() { echo "==> $*"; }

step "1/12 seeds from FALCONAge, 300BCG, 300OB"
$RUN python python/localseeds/extract_seeds.py 2>&1 | tee "$LOG/${STAMP}_01_seeds.log"

step "2/12 literature search (PubMed, Europe PMC, arXiv)"
if [[ $BROAD == 1 ]]; then
  python/pullliturature/run_search.sh all 2>&1 | tee "$LOG/${STAMP}_02_search.log"
else
  python/pullliturature/run_search.sh focused 2>&1 | tee "$LOG/${STAMP}_02_search.log"
fi

step "3/12 resolve seed DOIs not found by the searches"
$RUN python python/pullliturature/resolve_seeds.py 2>&1 | tee "$LOG/${STAMP}_03_resolve.log"

step "4/12 catalogue, first pass"
$RUN python python/buildcatalog/build_progress.py 2>&1 | tee "$LOG/${STAMP}_04_build.log"

step "5/12 OpenAlex citations and dates; preprint -> published links"
$RUN python python/pullliturature/enrich_openalex.py 2>&1 | tee "$LOG/${STAMP}_05_openalex.log"
$RUN python python/pullliturature/enrich_preprints.py 2>&1 | tee -a "$LOG/${STAMP}_05_openalex.log"

if [[ $FULLTEXT == 1 ]]; then
  step "6/12 open-access full texts ($TIERS)"
  $RUN python python/pullliturature/fetch_fulltext.py --tiers "$TIERS" --workers "$WORKERS" 2>&1 | tee "$LOG/${STAMP}_06_fulltext.log"
  step "   refresh catalogue with full-text paths"
  $RUN python python/buildcatalog/build_progress.py 2>&1 | tee -a "$LOG/${STAMP}_06_fulltext.log"
fi

step "7/12 code / data / website links in papers"
$RUN python python/pullrepos/mine_links.py 2>&1 | tee "$LOG/${STAMP}_07_links.log"

step "8/12 GitHub search"
$RUN python python/pullrepos/search_github.py 2>&1 | tee "$LOG/${STAMP}_08_github.log"

step "9/12 repositories and packages"
$RUN python python/pullrepos/build_repos.py 2>&1 | tee "$LOG/${STAMP}_09_repos.log"

step "10/12 websites"
$RUN python python/websites/build_websites.py 2>&1 | tee "$LOG/${STAMP}_10_websites.log"

step "11/12 catalogue, final pass"
$RUN python python/buildcatalog/build_progress.py 2>&1 | tee "$LOG/${STAMP}_11_build.log"

step "12/12 findings summary (<output>/summary/findings.md)"
$RUN python python/buildcatalog/summarise.py 2>&1 | tee "$LOG/${STAMP}_12_summary.log"

echo "done: $RESEARCH/progress.csv; new rows this run in $RESEARCH/changelog/added_$(date +%F).csv"
