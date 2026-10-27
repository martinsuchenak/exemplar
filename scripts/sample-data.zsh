#!/bin/zsh
# Fills an exemplar server with a sample set: four workspaces about one
# shared group of 20 registry people. One seed for all of them, so the same
# people are picked each time and a fresh instance gets identical content.
# Safe to run again: workspaces that already hold fixtures are left alone.
#
#   task sample-data                  # builds bin/exemplar, then runs this
#   EXEMPLAR_SERVER=https://exemplar.internal scripts/sample-data.zsh
#   scripts/sample-data.zsh --replace sample-records   # delete and regenerate one
#
# The key comes from EXEMPLAR_API_KEY, or is asked for (hidden) when unset.
set -euo pipefail

# --replace NAME (repeatable): delete that sample workspace first, so it is
# generated again.
typeset -A REPLACE
while (( $# )); do
  case $1 in
    --replace) REPLACE[$2]=1; shift 2 ;;
    *) echo "usage: $0 [--replace sample-emails|sample-documents|sample-records|sample-sales]..." >&2; exit 2 ;;
  esac
done

X=${EXEMPLAR_BIN:-${0:A:h}/../bin/exemplar}
SEED=2026
PEOPLE=20
export EXEMPLAR_SERVER=${EXEMPLAR_SERVER:-http://localhost:8080}

if [[ -z ${EXEMPLAR_API_KEY:-} ]]; then
  read -rs "EXEMPLAR_API_KEY?exemplar API key for $EXEMPLAR_SERVER (input hidden): "
  echo
  export EXEMPLAR_API_KEY
fi

# dataset NAME DESCRIPTION: prints the id of your workspace NAME, creating
# it if needed.
dataset() {
  local id
  id=$($X datasets list --name "$1" --scope workspace --json | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d[0]["id"] if d else "")')
  if [[ -z $id ]]; then
    id=$($X datasets create --name "$1" --description "$2" --tags sample --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
  fi
  print -r -- $id
}

fill() {
  local name=$1 desc=$2
  shift 2
  local id
  if (( ${+REPLACE[$name]} )); then
    id=$($X datasets list --name "$name" --scope workspace --json | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d[0]["id"] if d else "")')
    if [[ -n $id ]]; then
      $X datasets delete "$id"
      echo "== $name deleted ($id)"
    fi
  fi
  id=$(dataset "$name" "$desc")
  if [[ $($X fixtures list "$id" --limit 1 --json | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))') != 0 ]]; then
    echo "== $name already has fixtures; left as it is ($id)"
    return
  fi
  echo "== $name ($id)"
  $X generate "$id" "$@" --people $PEOPLE --seed $SEED --scenario sample
}

# Fails here, with the server's message, if the key is wrong.
$X accounts list >/dev/null

fill sample-emails "Every synthetic email shape, 4 of each" --kind emails --count 48
fill sample-documents "PDF, DOCX, XLSX, CSV and other files, 2 of each format" --kind files --count 20
fill sample-records "Contacts, companies, projects, opportunities and addresses, 6 of each" --kind records --count 30 \
  --schemas contact,company,project,opportunity,address
fill sample-sales "The product catalogue and 24 quotes priced from it" --kind records --count 48 --schemas product,quote

echo
echo "== Sample datasets"
$X datasets list --name sample-emails
$X datasets list --name sample-documents
$X datasets list --name sample-records
$X datasets list --name sample-sales
