#!/usr/bin/env bash
set -euo pipefail

probe_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repository_root=$(cd "${probe_dir}/../.." && pwd)
output_dir="${repository_root}/dist/databricks-apps-probe"

mkdir -p "${output_dir}"
cp "${probe_dir}/app.yaml" "${probe_dir}/bootstrap.py" "${output_dir}/"

for architecture in amd64 arm64; do
  binary="${output_dir}/metricspire-app-probe-linux-${architecture}"
  archive="${binary}.gz"
  GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH="${architecture}" \
    go build -trimpath -ldflags='-s -w' -o "${binary}" ./cmd/metricspire-app-probe
  gzip -9 -c "${binary}" > "${archive}"
  checksum=$(shasum -a 256 "${archive}" | cut -d ' ' -f 1)
  printf '%s  %s\n' "${checksum}" "$(basename "${archive}")" > "${archive}.sha256"
  rm "${binary}"
done

echo "Databricks Apps probe package: ${output_dir}"
