#!/usr/bin/env bash
# Parity check between the Python and the Go implementation of sourcelens.
#
#   parity/check.sh             every case in parity/cases.txt
#   parity/check.sh -k export   only cases containing "export"
#
# SOURCELENS_PY and SOURCELENS_GO name the two commands (defaults: sourcelens
# from the active Python environment, and bin/sourcelens built with `make go`).
# Each case runs through both; stdout, stderr and the exit code must match,
# apart from the implementation name, timestamps, durations and the {X}
# placeholder (py / go) in paths. Files written under {W}/ and under
# <catalogue>/exports/parity_{X}/ are compared too and removed afterwards.
set -uo pipefail
HERE="$(cd "$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
PY="${SOURCELENS_PY:-$(command -v sourcelens || echo "python3 -m sourcelens")}"
GO="${SOURCELENS_GO:-$ROOT/bin/sourcelens}"
FILTER=""
[[ "${1:-}" == "-k" ]] && FILTER="${2:-}"
W="$(mktemp -d)"
trap 'rm -rf "$W"' EXIT
OUT_BASE="$($GO config | sed -n 's/^output folder *//p')"

norm() {
  sed -E -e 's/sourcelens ([0-9.]+) \((python|go)\)/sourcelens \1 (IMPL)/' \
         -e 's#parity_(py|go)/#parity_X/#g; s#proj_(py|go)#proj_X#g' \
         -e "s#$W#{W}#g" \
         -e '/^\[[0-9]{2}:[0-9]{2}:[0-9]{2}\]/d' \
         -e 's/[0-9]{4}(_[0-9]{2}){5}/STAMP/g; s/cli_[0-9]{4}-[0-9]{2}-[0-9]{2}_[0-9]{6}/cli_STAMP/g' \
         -e 's/[0-9]+\.[0-9] min/N.N min/g'
}

pass=0; fail=0; n=0
while IFS= read -r line; do
  [[ -z "$line" || "$line" == \#* ]] && continue
  [[ -n "$FILTER" && "$line" != *"$FILTER"* ]] && continue
  n=$((n + 1))
  for impl in py go; do
    cmd="${line//\{X\}/$impl}"
    cmd="${cmd//\{W\}/$W}"
    exe="$PY"; [[ $impl == go ]] && exe="$GO"
    eval "set -- $cmd"
    $exe "$@" </dev/null >"$W/$n.$impl.out" 2>"$W/$n.$impl.err"
    echo "rc=$?" >>"$W/$n.$impl.out"
    norm <"$W/$n.$impl.out" >"$W/$n.$impl.out.n"
    norm <"$W/$n.$impl.err" >"$W/$n.$impl.err.n"
  done
  if cmp -s "$W/$n.py.out.n" "$W/$n.go.out.n" && cmp -s "$W/$n.py.err.n" "$W/$n.go.err.n"; then
    pass=$((pass + 1)); printf 'same  %s\n' "$line"
  else
    fail=$((fail + 1)); printf 'DIFF  %s\n' "$line"
    diff "$W/$n.py.out.n" "$W/$n.go.out.n" | head -20 | sed 's/^/      /'
    diff "$W/$n.py.err.n" "$W/$n.go.err.n" | head -10 | sed 's/^/      /'
  fi
done <"$HERE/cases.txt"

compare_dirs() {  # $1 label, $2 py dir, $3 go dir
  [[ -d "$2" || -d "$3" ]] || return 0
  # reports/ holds files named after the moment of each run; its dry-run tables are compared below
  if diff -r -x logs -x reports -x runs.csv -x "*.lock" -x records.jsonl.gz "$2" "$3" >"$W/files.diff" 2>&1; then
    pass=$((pass + 1)); echo "same  files: $1"
  else
    fail=$((fail + 1)); echo "DIFF  files: $1"; head -30 "$W/files.diff" | sed 's/^/      /'
  fi
}
compare_dirs "exports/parity_{X}/" "$OUT_BASE/exports/parity_py" "$OUT_BASE/exports/parity_go"
compare_dirs "{W}/proj_{X}/" "$W/proj_py" "$W/proj_go"
py_dry=$(ls "$W"/proj_py/reports/dryrun_*.csv 2>/dev/null | tail -1)
go_dry=$(ls "$W"/proj_go/reports/dryrun_*.csv 2>/dev/null | tail -1)
if [[ -n "$py_dry$go_dry" ]]; then
  if [[ -n "$py_dry" && -n "$go_dry" ]] && cmp -s "$py_dry" "$go_dry"; then
    pass=$((pass + 1)); echo "same  files: {W}/proj_{X}/reports/dryrun_STAMP.csv"
  else
    fail=$((fail + 1)); echo "DIFF  files: {W}/proj_{X}/reports/dryrun_STAMP.csv"
    diff "$py_dry" "$go_dry" | head -20 | sed 's/^/      /'
  fi
fi
rm -rf "$OUT_BASE/exports/parity_py" "$OUT_BASE/exports/parity_go"
echo "parity: $pass same, $fail different"
[[ $fail -eq 0 ]]
