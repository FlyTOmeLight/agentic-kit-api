#!/bin/sh
# Build the sandbox saver image: the layerfs checkpoint tool plus a Go
# wrapper, on the same digest-pinned server base image as the sandbox guard
# so python3 and the probe toolchain are identical to the verified probes.
# Run from the repo root.
#
# Produces agentic-sandbox-saver:<tag> for the Docker server's architecture.
# Set TARGETARCH to arm64 or amd64 to build for a different node pool.
set -eu

repo=$(CDPATH= cd -- "$(dirname -- "$0")/../../../.." && pwd)
tag=${1:-v1}
arch=${TARGETARCH:-$(docker version --format '{{.Server.Arch}}')}
case "$arch" in
  amd64|arm64) ;;
  *) echo "Unsupported TARGETARCH: $arch" >&2; exit 2 ;;
esac
build=${BUILD_DIR:-${TMPDIR:-/tmp}/agentic-saver-build-$arch}

mkdir -p "$build"

(cd "$repo" &&
  GOCACHE="$build/gocache" GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
    go build -trimpath -o "$build/saver" ./docs/sandbox-center/probes/saver)

cp "$repo/docs/sandbox-center/probes/kubernetes/testdata/layerfs_checkpoint.py" "$build/layerfs_checkpoint.py"

cat > "$build/Dockerfile.saver" <<'EOF'
FROM opensandbox/server@sha256:bcd5f264101262cebfa7e1ff9cd0424403e42f31a6596e4364cf90d8cb5c936b
COPY saver /usr/local/bin/saver
COPY layerfs_checkpoint.py /usr/local/bin/layerfs_checkpoint.py
ENTRYPOINT ["/usr/local/bin/saver"]
EOF

docker build --platform "linux/$arch" --network none --pull=false \
  -f "$build/Dockerfile.saver" -t "agentic-sandbox-saver:$tag" "$build"
docker image inspect "agentic-sandbox-saver:$tag" --format '{{.Id}}'
