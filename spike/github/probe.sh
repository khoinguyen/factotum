#!/usr/bin/env bash
# Probe the GitHub REST API for the factotum store-port mapping spike.
#
# Creates throwaway repositories on the authenticated account, exercises each
# primitive the report relies on, and deletes them on exit. Requires an
# authenticated `gh` (the token is read from `gh auth token`) whose token has
# the `delete_repo` scope, or the throwaway repos are left behind.
#
# Usage: ./probe.sh 2>&1 | tee transcript.txt
set -uo pipefail

OWNER="$(gh api user --jq .login)"
REPO="ft-spike-probe-$(date +%s)"
PUB="ft-spike-wiki-$(date +%s)"
TOK="$(gh auth token)"
API="repos/$OWNER/$REPO"
PUBAPI="repos/$OWNER/$PUB"

cleanup() {
  echo
  echo "== cleanup =="
  gh repo delete "$OWNER/$REPO" --yes >/dev/null 2>&1 && echo "deleted $OWNER/$REPO"
  gh repo delete "$OWNER/$PUB" --yes >/dev/null 2>&1 && echo "deleted $OWNER/$PUB"
}
trap cleanup EXIT

echo "== setup: $OWNER/$REPO (private, issues on, wiki toggle) =="
gh repo create "$OWNER/$REPO" --private -y >/dev/null 2>&1
gh api "$API" --jq '{full_name, private, has_issues, has_wiki}'

echo
echo "== TaskRepo: an issue is a task; PATCH mutates it; updated_at advances =="
gh api "$API/issues" -f title="Terraform notes" -f body="apply in devops" --jq '{number, state, updated_at}' | sed 's/^/  /'
NUM=$(gh api "$API/issues" --jq '.[0].number')
gh api -X PATCH "$API/issues/$NUM" -f title="Terraform notes v2" --jq '{number, title, updated_at}' | sed 's/^/  /'

echo
echo "== TaskRepo.Delete: DELETE /issues/{n} is not available on a user-owned repo =="
DEL=$(gh api "$API/issues" -f title="delete probe" --jq '.number')
curl -s -o /dev/null -w "  DELETE issue -> HTTP %{http_code}\n" -X DELETE \
  -H "Authorization: Bearer $TOK" -H "Accept: application/vnd.github+json" \
  "https://api.github.com/$API/issues/$DEL"
curl -s -o /dev/null -w "  GET after DELETE -> HTTP %{http_code}\n" \
  -H "Authorization: Bearer $TOK" "https://api.github.com/$API/issues/$DEL"
gh api -X POST "$API/labels" -f name="tmp" -f color=ededed >/dev/null 2>&1
curl -s -o /dev/null -w "  DELETE label -> HTTP %{http_code} (labels are hard-deletable)\n" -X DELETE \
  -H "Authorization: Bearer $TOK" "https://api.github.com/$API/labels/tmp"

echo
echo "== UpdateExpected/CAS: issue writes reject If-Match outright =="
curl -s -X PATCH -H "Authorization: Bearer $TOK" -H "Accept: application/vnd.github+json" \
  -H 'If-Match: "anything"' "https://api.github.com/$API/issues/$NUM" \
  -d '{"title":"cas attempt"}' | jq -c '{message, errors}' | sed 's/^/  /'
ETAG=$(curl -s -D - -o /dev/null -H "Authorization: Bearer $TOK" \
  "https://api.github.com/$API/issues/$NUM" | awk -F': ' 'tolower($1)=="etag"{print $2}' | tr -d '\r')
curl -s -o /dev/null -w "  conditional GET (If-None-Match) -> HTTP %{http_code}\n" \
  -H "Authorization: Bearer $TOK" -H "If-None-Match: $ETAG" "https://api.github.com/$API/issues/$NUM"

echo
echo "== Labels: spaces are legal, commas are not =="
gh api -X POST "$API/labels" -f name="needs grooming" -f color=ededed --jq .name | sed 's/^/  space label: /'
curl -s -X POST -H "Authorization: Bearer $TOK" -H "Accept: application/vnd.github+json" \
  "$API/labels" -d '{"name":"a,b","color":"ededed"}' | jq -c '{status, errors}' | sed 's/^/  comma label: /'

echo
echo "== Milestone: a real repo-scoped object with a due date =="
gh api -X POST "$API/milestones" -f title="release 1" -f due_on="2026-12-01T00:00:00Z" --jq '{number,title,due_on}' | sed 's/^/  /'

echo
echo "== DependsOn reverse edge: native issue dependencies =="
A=$(gh api "$API/issues" -f title="dep A" --jq .number)
B=$(gh api "$API/issues" -f title="dep B" --jq .number)
C=$(gh api "$API/issues" -f title="dep C" --jq .number)
AID=$(gh api "$API/issues/$A" --jq .id)
BID=$(gh api "$API/issues/$B" --jq .id)
gh api -X POST "$API/issues/$B/dependencies/blocked_by" -F issue_id="$AID" >/dev/null
gh api -X POST "$API/issues/$C/dependencies/blocked_by" -F issue_id="$AID" >/dev/null
gh api -X POST "$API/issues/$C/dependencies/blocked_by" -F issue_id="$BID" >/dev/null
echo "  dependents of A (GET issues/$A/dependencies/blocking) = $(gh api "$API/issues/$A/dependencies/blocking" --jq -c '[.[]|.number]')  (want [B,C]=[$B,$C])"

echo
echo "== Search: no token-prefix; multi-term AND; eventual; low rate limit =="
gh api "$API/issues" -f title="run scripts" -f body="terraform then kubectl" >/dev/null
sleep 6
SRCH="https://api.github.com/search/issues"
q() { curl -s -H "Authorization: Bearer $TOK" -H "Accept: application/vnd.github+json" "$SRCH?q=$1" | jq -c '{total:.total_count, items:[.items[].number]}'; }
echo "  terra (prefix of terraform) -> $(q "repo:$OWNER/$REPO+is:issue+terra")"
echo "  terraform                   -> $(q "repo:$OWNER/$REPO+is:issue+terraform")"
echo "  terraform apply (AND)       -> $(q "repo:$OWNER/$REPO+is:issue+terraform+apply")"
gh api rate_limit --jq '"  search budget: \(.resources.search.limit)/min"' | sed 's/^/ /'

echo
echo "== ArtifactRepo for wiki pages =="
echo "  private repo has_wiki after enabling: $(gh api -X PATCH "$API" -f has_wiki=true --jq .has_wiki), re-read: $(gh api "$API" --jq .has_wiki)"
gh repo create "$OWNER/$PUB" --public --enable-wiki -y >/dev/null 2>&1
echo "  public repo has_wiki: $(gh api "$PUBAPI" --jq .has_wiki)"
curl -s -o /dev/null -w "  REST /wiki -> HTTP %{http_code} (no wiki API)\n" -H "Authorization: Bearer $TOK" "https://api.github.com/$PUBAPI/wiki"
git clone "https://x-access-token:$TOK@github.com/$OWNER/$PUB.wiki.git" /tmp/ft-spike-wiki-clone >/dev/null 2>&1 \
  && echo "  empty .wiki.git clone: ok" \
  || echo "  empty .wiki.git clone: FAILED (first page must be created in the web UI first)"

echo
echo "== ActorRepo: GitHub users/bots have a stable numeric id, no active flag =="
gh api "users/$OWNER" --jq '{id, login, type, created_at}' | sed 's/^/  /'
gh api "users/github-actions%5Bbot%5D" --jq '{id, login, type}' | sed 's/^/  /'
