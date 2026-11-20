#!/bin/sh
# Build the phase-2 sandbox runtime image: fixed OpenSandbox commit + UDS patch
# + Guard, on the digest-pinned server base image. Run from the repo root.
#
# Produces agentic-guard-execd:<tag> for the Docker server's architecture.
# Set TARGETARCH to arm64 or amd64 to build for a different node pool.
set -eu

repo=$(CDPATH= cd -- "$(dirname -- "$0")/../../../.." && pwd)
tag=${1:-phase2}
commit=b7d7697750b45be2ccc876da645c8880a0cf69fc
arch=${TARGETARCH:-$(docker version --format '{{.Server.Arch}}')}
case "$arch" in
  amd64|arm64) ;;
  *) echo "Unsupported TARGETARCH: $arch" >&2; exit 2 ;;
esac
build=${BUILD_DIR:-${TMPDIR:-/tmp}/agentic-guard-build-$arch}
source_dir="$build/OpenSandbox-$commit"

mkdir -p "$build"
if [ ! -d "$source_dir" ]; then
  curl -fsSL "https://codeload.github.com/opensandbox-group/OpenSandbox/tar.gz/$commit" -o "$build/source.tar.gz"
  tar -xzf "$build/source.tar.gz" -C "$build"
  git -C "$source_dir" init -q .
  git -C "$source_dir" apply "$repo/docs/sandbox-center/probes/guard-execd/patches/execd-uds.patch"
fi

(cd "$source_dir/components/execd" &&
  GOWORK=off GOMODCACHE="$build/modcache" go mod download &&
  GOWORK=off GOMODCACHE="$build/modcache" CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
    go build -trimpath -o "$build/execd" .)

(cd "$repo" &&
  GOCACHE="$build/gocache" GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
    go build -trimpath -o "$build/guard" ./docs/sandbox-center/probes/guard-execd)

cat > "$build/Dockerfile" <<'EOF'
FROM opensandbox/server@sha256:bcd5f264101262cebfa7e1ff9cd0424403e42f31a6596e4364cf90d8cb5c936b
COPY guard /guard
COPY execd /execd
ENTRYPOINT ["/guard"]
EOF

docker build --platform "linux/$arch" --network none --pull=false -t "agentic-guard-execd:$tag" "$build"
docker image inspect "agentic-guard-execd:$tag" --format '{{.Id}}'
