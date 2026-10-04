#!/usr/bin/env bash
# Parity check between the two litSearch implementations.
#
#   parity/check.sh            run every case in parity/cases.txt
#   parity/check.sh -k export  only cases containing "export"
#
# Each case runs through python/litSearch and go/litSearch; stdout, stderr and
# the exit code must be identical, apart from the implementation name in the
# version line and the {X} placeholder (py / go) in output paths. Files written
# under <output folder>/exports/parity_{X}/ are compared as well and removed.
# This script belongs to neither implementation and shares no code with them.
set -uo pipefail
HERE="$(cd "$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
FILTER=""
[[ "${1:-}" == "-k" ]] && FILTER="${2:-}"
RESEARCH="${LITSEARCH_RESEARCH:-$(sed -n 's/^research_dir:[[:space:]]*//p' "$ROOT/config/local.yaml" | head -1 | sed -e 's/[[:space:]]#.*$//' -e 's/^"\(.*\)"$/\1/')}"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# build both images up front so build chatter never lands in a case's output
"$ROOT/python/run.sh" true >/dev/null 2>&1 || "$ROOT/python/run.sh" build
"$ROOT/go/run.sh" true >/dev/null 2>&1 || "$ROOT/go/run.sh" build

norm() { sed -e 's/litSearch 2\.0 (python)/litSearch 2.0 (IMPL)/; s/litSearch 2\.0 (go)/litSearch 2.0 (IMPL)/' \
             -e 's#parity_py/#parity_X/#g; s#parity_go/#parity_X/#g'; }

pass=0; fail=0; n=0
while IFS= read -r line; do
  [[ -z "$line" || "$line" == \#* ]] && continue
  [[ -n "$FILTER" && "$line" != *"$FILTER"* ]] && continue
  n=$((n + 1))
  for impl in py go; do
    cmd="${line//\{X\}/$impl}"
    launcher="$ROOT/python/litSearch"; [[ $impl == go ]] && launcher="$ROOT/go/litSearch"
    eval "set -- $cmd"
    "$launcher" "$@" </dev/null >"$WORK/$n.$impl.out" 2>"$WORK/$n.$impl.err"
    echo "rc=$?" >>"$WORK/$n.$impl.out"
    norm <"$WORK/$n.$impl.out" >"$WORK/$n.$impl.out.n"
    # step logs (timestamps) are not part of the comparison
    grep -v '^\[[0-9][0-9]:[0-9][0-9]:[0-9][0-9]\]' "$WORK/$n.$impl.err" | norm >"$WORK/$n.$impl.err.n"
  done
  if cmp -s "$WORK/$n.py.out.n" "$WORK/$n.go.out.n" && cmp -s "$WORK/$n.py.err.n" "$WORK/$n.go.err.n"; then
    pass=$((pass + 1)); printf 'same  %s\n' "$line"
  else
    fail=$((fail + 1)); printf 'DIFF  %s\n' "$line"
    diff "$WORK/$n.py.out.n" "$WORK/$n.go.out.n" | head -20 | sed 's/^/      /'
    diff "$WORK/$n.py.err.n" "$WORK/$n.go.err.n" | head -10 | sed 's/^/      /'
  fi
done <"$HERE/cases.txt"

if [[ -d "$RESEARCH/exports/parity_py" || -d "$RESEARCH/exports/parity_go" ]]; then
  if diff -r "$RESEARCH/exports/parity_py" "$RESEARCH/exports/parity_go" >"$WORK/files.diff"; then
    pass=$((pass + 1)); echo "same  files written under exports/parity_{X}/"
  else
    fail=$((fail + 1)); echo "DIFF  files written under exports/parity_{X}/"; head -30 "$WORK/files.diff" | sed 's/^/      /'
  fi
  rm -rf "$RESEARCH/exports/parity_py" "$RESEARCH/exports/parity_go"
fi
echo "parity: $pass same, $fail different"
[[ $fail -eq 0 ]]
