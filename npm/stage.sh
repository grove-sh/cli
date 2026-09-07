#!/usr/bin/env bash
# Assemble the npm packages for a release into a directory, ready to publish.
#
#   npm/stage.sh <version> <outdir>
#
# Five packages come out: one per platform holding a binary, and the wrapper
# that depends on all four and picks the one that installed. Nothing tracked by
# git is modified, so the same script serves a real release and a rehearsal
# against a local registry.
set -euo pipefail

if [ $# -ne 2 ]; then
  echo "usage: $0 <version> <outdir>" >&2
  exit 2
fi

# A git tag says v0.1.0 and npm says 0.1.0. Take either, and stamp the binary
# with the v, since that is what Go's own versions look like.
version=${1#v}
out=$2
root=$(cd "$(dirname "$0")/.." && pwd)

# npm's names, not Go's: the shim reads process.platform and os.arch().
platforms="darwin:arm64:darwin:arm64 darwin:amd64:darwin:x64 linux:arm64:linux:arm64 linux:amd64:linux:x64"

mkdir -p "$out"

for entry in $platforms; do
  IFS=: read -r goos goarch os cpu <<<"$entry"
  name="$os-$cpu"
  dir="$out/cli-$name"
  mkdir -p "$dir/bin"

  # No cgo, so one machine builds all four. -trimpath keeps the paths of
  # whatever built it out of the binary, and -s -w drop the debug information
  # a user of a release has no way to use. Go tracebacks survive both.
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath -ldflags "-s -w -X main.customVersion=v$version" \
    -o "$dir/bin/grove" "$root/cmd/grove"
  chmod +x "$dir/bin/grove"

  sed -e "s/0\.0\.0/$version/g" \
      -e "s/PLATFORM/$name/g" \
      -e "s/\"OS\"/\"$os\"/" \
      -e "s/\"CPU\"/\"$cpu\"/" \
      "$root/npm/platform/package.json" > "$dir/package.json"
  sed -e "s/PLATFORM/$name/g" "$root/npm/platform/README.md" > "$dir/README.md"
  cp "$root/LICENSE" "$dir/LICENSE"

  echo "staged @grove-sh/cli-$name  $(du -h "$dir/bin/grove" | cut -f1)"
done

mkdir -p "$out/cli/bin"
cp "$root/npm/cli/bin/grove.js" "$out/cli/bin/grove.js"
chmod +x "$out/cli/bin/grove.js"
sed -e "s/0\.0\.0/$version/g" "$root/npm/cli/package.json" > "$out/cli/package.json"
cp "$root/npm/cli/README.md" "$out/cli/README.md"
cp "$root/LICENSE" "$out/cli/LICENSE"
echo "staged @grove-sh/cli"

# For whatever publishes these: the wrapper goes last. Its optionalDependencies
# are pinned exactly, so publishing it first would put a manifest on the
# registry naming four versions that do not exist yet.
echo "$version" > "$out/VERSION"
