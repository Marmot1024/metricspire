#!/usr/bin/env bash
set -euo pipefail

package_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repository_root=$(cd "${package_dir}/../.." && pwd)
output_dir="${repository_root}/dist/databricks-apps"

rm -rf "${output_dir}"
mkdir -p "${output_dir}/config"

python3 "${package_dir}/render.py" "${output_dir}"
cp "${package_dir}/bootstrap.py" "${output_dir}/"
cp "${repository_root}/testdata/acceptance/databricks-tpch/policy.yaml" "${output_dir}/config/policy.yaml"
cp "${repository_root}/testdata/acceptance/databricks-tpch/binding.json" "${output_dir}/config/binding.json"

for architecture in amd64 arm64; do
  binary="${output_dir}/metricspire-linux-${architecture}"
  archive="${binary}.gz"
  GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH="${architecture}" \
    go build -trimpath -ldflags='-s -w' -o "${binary}" ./cmd/metricspire
  gzip -9 -c "${binary}" > "${archive}"
  checksum=$(shasum -a 256 "${archive}" | cut -d ' ' -f 1)
  printf '%s  %s\n' "${checksum}" "$(basename "${archive}")" > "${archive}.sha256"
  rm "${binary}"
done

echo "Databricks Apps staging package: ${output_dir}"
