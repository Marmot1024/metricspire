#!/bin/sh
# Offline walkthrough using the same compiler and planner as the service.
set -eu
repo_dir=$(CDPATH='' cd "$(dirname "$0")/.." && pwd)
cd "$repo_dir"
mkdir -p dist
output_dir=$(mktemp -d "$repo_dir/dist/quickstart.XXXXXX")

go run ./cmd/metricspire compile \
  --source examples/orders/model.yaml --out "$output_dir/manifest.json"
go run ./cmd/metricspire compile-policy \
  --source examples/orders/policy.yaml --manifest "$output_dir/manifest.json" \
  --out "$output_dir/policy.json"
go run ./cmd/metricspire plan \
  --manifest "$output_dir/manifest.json" --policy "$output_dir/policy.json" \
  --context examples/orders/context.json --query examples/orders/query.json \
  --binding examples/orders/binding.json --capabilities examples/orders/capabilities.json \
  --logical-out "$output_dir/logical-plan.json" --physical-out "$output_dir/physical-plan.json"

printf '\nPlan created from the example definitions, policy and query:\n'
cat "$output_dir/logical-plan.json"
printf '\nFiles: %s\n' "$output_dir"
printf 'Offline example complete. No database connection or data query was made.\n'
