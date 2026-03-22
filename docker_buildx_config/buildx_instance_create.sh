#!/usr/bin/env bash
set -euo pipefail

BUILDER_NAME="multiarch"
CONFIG_FILE="./buildkitd.toml"

echo "==> step 1/5: install qemu/binfmt for cross-platform builds"
docker run --privileged --rm tonistiigi/binfmt --install all

echo "==> step 2/5: remove old builder if exists"
docker buildx rm "${BUILDER_NAME}" 2>/dev/null || true

echo "==> step 3/5: create buildx builder --> ${BUILDER_NAME}"
docker buildx create \
  --name "${BUILDER_NAME}" \
  --driver docker-container \
  --buildkitd-config "${CONFIG_FILE}" \
  --use

echo "==> step 4/5: bootstrap builder --> ${BUILDER_NAME}"
docker buildx inspect --bootstrap

echo "==> step 5/5: show builders"
docker buildx ls

echo "==> supported platforms for ${BUILDER_NAME}:"
docker buildx inspect "${BUILDER_NAME}" --bootstrap | sed -n '/Platforms:/p'

echo
echo "If you can see linux/arm64 and/or linux/arm/v7 above, the multi-arch builder is ready."
