#!/usr/bin/env bash
# Publish the npm packages to a throwaway registry, install them the way a user
# would, and check that what comes out the far side actually works.
#
#   npm/rehearse.sh
#
# npm cannot be undone: a version is gone after 72 hours and can never be
# reused. So the class of bug this catches, a binary that shipped without its
# executable bit, a shim that resolves nothing, a platform package that
# installs on the wrong machine, is worth catching against a registry that
# costs nothing to get wrong. Nothing here touches npmjs.com, the real npm
# cache, or a grove already running on this machine.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/grove-rehearsal.XXXXXX")
version="0.0.0-rehearsal.$(date +%s)"
failures=0

case "$(uname -s)" in
  Darwin) host_os=darwin ;;
  Linux) host_os=linux ;;
  *) echo "no grove build for $(uname -s)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) host_cpu=x64 ;;
  arm64 | aarch64) host_cpu=arm64 ;;
  *) echo "no grove build for $(uname -m)" >&2; exit 1 ;;
esac
host="$host_os-$host_cpu"

# write, not console.log: that runs a number through util.inspect, which paints
# it, and an ANSI escape in the middle of a port number is a long way to debug.
free_port() { node -e 'const s=require("net").createServer();s.listen(0,"127.0.0.1",()=>{process.stdout.write(String(s.address().port));s.close()})'; }

verdaccio_pid=
daemon_pid=
cleanup() {
  [ -n "$daemon_pid" ] && kill "$daemon_pid" 2>/dev/null || true
  [ -n "$verdaccio_pid" ] && kill "$verdaccio_pid" 2>/dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT

ok() { printf '  ok    %s\n' "$1"; }
no() { printf '  FAIL  %s\n' "$1" >&2; failures=$((failures + 1)); }

echo "rehearsing $version on $host, in $work"

# ---------------------------------------------------------------- the registry
port=$(free_port)
registry="http://localhost:$port/"
mkdir -p "$work/storage"
cat > "$work/verdaccio.yaml" <<EOF
storage: $work/storage
max_body_size: 300mb
# No uplink: nothing but @grove-sh/* is ever asked for, and a registry that
# cannot reach the internet cannot accidentally publish to it.
uplinks: {}
packages:
  '**':
    access: \$all
    publish: \$all
    unpublish: \$all
log: { type: stdout, format: pretty, level: warn }
EOF

# A token nothing checks, because the registry above lets anyone publish. npm
# refuses to publish without one being configured, which is the only reason it
# is here. A private cache too, so a rehearsal's bytes can never be served for
# a real version later.
cat > "$work/npmrc" <<EOF
registry=$registry
//localhost:$port/:_authToken=rehearsal
cache=$work/npm-cache
EOF
# Named on each command rather than exported. npx has to reach the real
# registry to fetch verdaccio, and pointing it at a verdaccio that is not
# running yet is a circle with no way in.
local_npm() { NPM_CONFIG_USERCONFIG="$work/npmrc" npm "$@"; }

echo "starting verdaccio on $port"
npx --yes verdaccio@6 --config "$work/verdaccio.yaml" --listen "$port" > "$work/verdaccio.log" 2>&1 &
verdaccio_pid=$!
for _ in $(seq 1 60); do
  curl -sf "$registry" -o /dev/null && break
  sleep 1
done
if ! curl -sf "$registry" -o /dev/null; then
  echo "verdaccio never answered:" >&2
  tail -20 "$work/verdaccio.log" >&2
  exit 1
fi

# ----------------------------------------------------------------- the publish
"$root/npm/stage.sh" "$version" "$work/packages"

# The platform packages first: the wrapper pins them exactly, so publishing it
# before them would put a manifest on the registry naming versions that do not
# exist. --tag keeps a rehearsal off "latest", which npm also insists on for a
# prerelease version.
publish() { # npm says everything on stderr, including what went well
  if ! (cd "$1" && local_npm publish --tag rehearsal --registry "$registry") > "$work/publish.log" 2>&1; then
    echo "publishing $1 failed:" >&2
    cat "$work/publish.log" >&2
    exit 1
  fi
}
for dir in "$work"/packages/cli-*; do publish "$dir"; done
publish "$work/packages/cli"
echo "published 5 packages"

# ------------------------------------------------------ what the registry keeps
# Installing only ever proves the build for this machine. The other three are
# what a release ships to people who cannot run them here, and a mode lost on
# the way through a registry stays invisible until one of them tries.
echo
echo "what the registry serves:"
mkdir -p "$work/tarballs"

# The name npm packs to, spelled out rather than matched. A glob for the
# wrapper's tarball also matches every platform one, since cli- is a prefix of
# cli-darwin-arm64, and which of them a glob returns first is up to the
# filesystem: that passed here and failed on a CI runner.
pack() { # package name without the scope; echoes the tarball it wrote
  (cd "$work/tarballs" && local_npm pack "@grove-sh/$1@$version" --registry "$registry" > /dev/null 2>&1)
  echo "$work/tarballs/grove-sh-$1-$version.tgz"
}

mode_of() { # a tar -tzvf listing, and a path inside the package
  awk -v want="package/$2" '$NF == want { print $1 }' <<< "$1"
}

check() { # package name without the scope, and the file that must be runnable
  local tgz listing mode
  tgz=$(pack "$1")
  if [ ! -f "$tgz" ]; then
    no "$1 is not in the registry"
    return
  fi
  listing=$(tar -tzvf "$tgz")
  mode=$(mode_of "$listing" "$2")
  case "$mode" in
    -rwx*) ok "$1 ships $2 executable" ;;
    "") no "$1 ships no $2 at all" ;;
    *) no "$1 ships $2 as $mode, which nothing can run" ;;
  esac

  # The wrapper is a shim and four dependencies. A binary inside it would put
  # 11MB into every install of a package meant to be a few kilobytes.
  if [ "$1" = "cli" ]; then
    if grep -q 'package/bin/grove$' <<< "$listing"; then
      no "cli carries a binary of its own"
    else
      ok "cli carries no binary of its own"
    fi
  fi
}

for name in darwin-arm64 darwin-x64 linux-arm64 linux-x64; do
  check "cli-$name" bin/grove
done
check cli bin/grove.js

# ----------------------------------------------------------------- the install
prefix="$work/global"
local_npm install -g --prefix "$prefix" --registry "$registry" "@grove-sh/cli@$version" > /dev/null
bin="$prefix/bin/grove"

echo
echo "what a user gets:"

if [ -x "$bin" ]; then ok "grove is on the path"; else no "no grove at $bin"; fi

reported=$("$bin" --version 2>&1 || true)
if [ "$reported" = "grove v$version" ]; then
  ok "reports the version it was built as"
else
  no "reports '$reported', not 'grove v$version'"
fi

# Only the one build for this machine, which is what os and cpu are for. Three
# extra binaries on every install would be 30MB of nothing.
installed=$(find "$prefix" -type d -name 'cli-*-*' -exec basename {} \; | sort | tr '\n' ' ')
if [ "$installed" = "cli-$host " ]; then
  ok "installed only the $host build"
else
  no "installed '$installed', wanted 'cli-$host '"
fi

binary=$(find "$prefix" -type f -path '*cli-*/bin/grove' | head -1)
if [ -x "$binary" ]; then
  ok "the binary kept its executable bit through the registry"
else
  no "the binary is not executable: $(ls -l "$binary" 2>&1)"
fi

# -------------------------------------------------------------------- signals
# The shim adds a process between the terminal and grove, so a signal has one
# more hop to make than it would with a bare binary. grove exec relays what it
# is sent to the command it is running, and none of that reaches the command if
# the shim dies to the default action first. --if-available at a socket nothing
# answers is the one path that needs no daemon and no config.
"$bin" exec --if-available --autostart=false --socket "$work/nothing.sock" -- sleep 987 > /dev/null 2>&1 &
shim=$!
sleep 2
kill -TERM "$shim" 2>/dev/null || true
set +e
wait "$shim"
signalled=$?
set -e
if [ "$signalled" -eq 143 ]; then
  ok "a signal to the shim comes back as 128+SIGTERM"
else
  no "the shim exited $signalled, not 143"
fi
sleep 1
if pgrep -f "sleep 987" > /dev/null 2>&1; then
  no "the command outlived the signal"
else
  ok "the signal reached the command grove was running"
fi

# ----------------------------------------------------------------- the daemon
# grove's daemon has to outlive the process that started it, and under npm that
# process is node. It re-executes os.Executable(), so what must come back is the
# Go binary detached from everything, not a child of a node that has exited.
export GROVE_STATE_DIR="$work/state"
export GROVE_SOCKET="$work/state/grove.sock"
https_port=$(free_port)
http_port=$(free_port)
export GROVE_LISTEN="127.0.0.1:$https_port" GROVE_HTTP_LISTEN="127.0.0.1:$http_port"
mkdir -p "$GROVE_STATE_DIR"

"$bin" install --trust=false > /dev/null 2>&1 || true
started=$("$bin" start 2>&1 || true)
# From a second start, which reports the daemon it found. The first start's
# report is written for a person and says nothing a script should depend on.
found=$("$bin" start 2>&1 || true)
daemon_pid=$(printf '%s' "$found" | sed -n 's/.*pid \([0-9][0-9]*\).*/\1/p' | head -1)

if [ -z "$daemon_pid" ]; then
  no "the daemon did not start: $started$found"
else
  parent=$(ps -o ppid= -p "$daemon_pid" 2>/dev/null | tr -d ' ')
  if [ "$parent" = "1" ]; then
    ok "the daemon detached, and did not die with the shim"
  else
    no "the daemon's parent is $parent, not init: it is still under the shim"
  fi

  # Linux only, and worth it: proof that what is serving is the Go binary out
  # of the platform package rather than anything node is holding open.
  if [ -r "/proc/$daemon_pid/exe" ]; then
    running=$(readlink "/proc/$daemon_pid/exe")
    case "$running" in
      *"cli-$host/bin/grove") ok "the daemon is the binary from @grove-sh/cli-$host" ;;
      *) no "the daemon is running $running" ;;
    esac
  fi

  "$bin" doctor > "$work/doctor.txt" 2>&1 || true
  if grep -q '^Grove *running' "$work/doctor.txt"; then
    ok "doctor finds it"
  else
    no "doctor did not find the daemon it just started:"
    sed 's/^/        /' "$work/doctor.txt" >&2
  fi
  # A daemon whose version does not match this CLI reads as an upgrade nobody
  # restarted after, which is exactly what it would be if the shim ran one
  # build and the daemon another.
  if grep -q 'was built from' "$work/doctor.txt"; then
    no "doctor thinks the daemon is a different build from the CLI"
  else
    ok "the CLI and the daemon are the same build"
  fi

  "$bin" stop > /dev/null 2>&1 || true
fi

echo
if [ "$failures" -eq 0 ]; then
  echo "the packages are publishable"
else
  echo "$failures problem(s): do not publish" >&2
  exit 1
fi
