#!/usr/bin/env bash
# Reproducible release build of `good`: the same commit always gives the same tarballs, byte for byte.
#
# usage: scripts/build-dist.sh <commit> [outdir] [os/arch ...]
#
# Writes <outdir>/good-<sha12>-<os>-<arch>.tar.gz (one `good` binary each) and <outdir>/checksums.txt.
# Defaults: outdir=dist, targets linux/amd64 darwin/arm64 darwin/amd64.
#
# main.version must differ per commit: golangci-lint salts its lint cache with it, so a constant
# version string over changing code would serve stale cache entries.
set -euo pipefail

if [ "$#" -lt 1 ]; then
  echo "usage: $0 <commit> [outdir] [os/arch ...]" >&2
  exit 2
fi

cd "$(dirname "$0")/.."

sha=$(git rev-parse --verify "$1^{commit}")
short=${sha:0:12}
outdir=${2:-dist}
if [ "$#" -ge 2 ]; then
  shift 2
else
  shift
fi
targets=("$@")
if [ "${#targets[@]}" -eq 0 ]; then
  targets=(linux/amd64 darwin/arm64 darwin/amd64)
fi

mkdir -p "$outdir"
outdir=$(cd "$outdir" && pwd)

# Newer than the go.mod `go` line: golangci-lint refuses to lint code that targets a newer Go.
export GOTOOLCHAIN=${GOOD_GOTOOLCHAIN:-go1.27.3}
export CGO_ENABLED=0
export GOWORK=off
export GOFLAGS=-mod=readonly
export GOEXPERIMENT=
export GOAMD64=v1
export GOARM64=v8.0

commit_time=$(git show -s --format=%ct "$sha")
commit_date=$(TZ=UTC git show -s --format=%cd --date=format-local:%Y-%m-%dT%H:%M:%SZ "$sha")

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

git archive --format=tar "$sha" | tar -x -C "$work"

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  else
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

pack() {
  python3 - "$1" "$2" "$commit_time" <<'PY'
import gzip
import os
import sys
import tarfile

binary, archive, mtime = sys.argv[1], sys.argv[2], int(sys.argv[3])

with open(archive, "wb") as raw:
    with gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=0, compresslevel=9) as compressed:
        with tarfile.open(fileobj=compressed, mode="w", format=tarfile.USTAR_FORMAT) as tar:
            info = tarfile.TarInfo("good")
            info.size = os.path.getsize(binary)
            info.mtime = mtime
            info.mode = 0o755
            info.uid = info.gid = 0
            info.uname = info.gname = ""
            with open(binary, "rb") as source:
                tar.addfile(info, source)
PY
}

checksums="$outdir/checksums.txt"
: > "$checksums"

for target in "${targets[@]}"; do
  goos=${target%/*}
  goarch=${target#*/}
  name="good-$short-$goos-$goarch"

  (
    cd "$work"
    GOOS=$goos GOARCH=$goarch go build -trimpath \
      -ldflags "-s -w -X main.version=good-$short -X main.commit=$sha -X main.date=$commit_date" \
      -o "$work/good-$goos-$goarch" ./cmd/golangci-lint
  )

  pack "$work/good-$goos-$goarch" "$outdir/$name.tar.gz"
  echo "binary  $(sha256_of "$work/good-$goos-$goarch")  $name" >&2
  echo "$(sha256_of "$outdir/$name.tar.gz")  $name.tar.gz" >> "$checksums"
done

cat "$checksums"
