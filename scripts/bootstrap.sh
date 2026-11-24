#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
docker compose up -d --wait spire-server
token="${SPIRE_JOIN_TOKEN:-}"
if [[ -z "$token" && -f .env ]]; then
  token="$(sed -n 's/^SPIRE_JOIN_TOKEN=//p' .env | tail -n 1)"
fi
if [[ -z "$token" ]]; then
  token="$(docker compose exec -T spire-server /opt/spire/bin/spire-server token generate | awk '/Token:/ {print $2}')"
  if [[ -f .env ]] && grep -q '^SPIRE_JOIN_TOKEN=' .env; then
    awk -v token="$token" 'BEGIN { replaced = 0 } /^SPIRE_JOIN_TOKEN=/ { if (!replaced) print "SPIRE_JOIN_TOKEN=" token; replaced = 1; next } { print } END { if (!replaced) print "SPIRE_JOIN_TOKEN=" token }' .env > .env.tmp
    mv .env.tmp .env
  else
    printf 'SPIRE_JOIN_TOKEN=%s\n' "$token" >> .env
  fi
fi
if [[ -z "$token" ]]; then
  echo "Could not obtain SPIRE join token" >&2
  exit 1
fi
parent_id="spiffe://lab.local/spire/agent/join_token/$token"
for spec in \
  'spiffe://lab.local/ns/finance/sa/ledger-server docker:label:app:ledger-server' \
  'spiffe://lab.local/ns/orders/sa/order-client docker:label:app:order-client' \
  'spiffe://lab.local/ns/external/sa/rogue-client docker:label:app:rogue-client'; do
  read -r id selector <<<"$spec"
  existing_ids="$(docker compose exec -T spire-server /opt/spire/bin/spire-server entry show -spiffeID "$id" 2>/dev/null | sed -n 's/^Entry ID[[:space:]]*:[[:space:]]*//p')"
  while IFS= read -r entry_id; do
    [[ -z "$entry_id" ]] || docker compose exec -T spire-server /opt/spire/bin/spire-server entry delete -entryID "$entry_id"
  done <<<"$existing_ids"
  docker compose exec -T spire-server /opt/spire/bin/spire-server entry create \
    -parentID "$parent_id" -spiffeID "$id" -selector "$selector"
done
export SPIRE_JOIN_TOKEN="$token"
docker compose up -d --build spire-agent ledger-server order-client
echo "SPIRE lab started. Rogue identity is available with: docker compose run --rm rogue-client"
