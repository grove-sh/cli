# Grove

A single Go binary that gives every git worktree its own HTTPS hostname, its own leased ports, and its own environment, so several checkouts of a repository run at once without colliding.

## Commands

```sh
go build ./cmd/grove          # the binary, gitignored at the repo root
go test -race -count=1 ./...  # what CI runs
gofmt -l .                    # CI fails on any output here; so does go vet ./...
npm/rehearse.sh               # the npm packaging, needs node and go
```

After rebuilding, `grove restart` before testing a change by hand. A running daemon keeps serving the build it started as, and `daemon.Version` only moves when the wire shape does, so almost every rebuild leaves the old behaviour in place silently rather than reporting a mismatch.

Nothing here uses cgo, which is what makes cross-compiling linux and darwin against amd64 and arm64 a plain `go build`; the build matrix sets `CGO_ENABLED=0`, so a dependency needing cgo is not an option. CI also sets `GOTOOLCHAIN=local` to use the runner's Go rather than downloading the one named in go.mod, so raising the `go` directive past what the GitHub runners ship breaks CI instead of quietly fetching a toolchain.

CI also runs the suite with `TMPDIR` pointed through a symlink, because macOS reaches every temp directory that way (`/var` resolves to `/private/var`) and git reports resolved paths. Path comparison bugs show up there and nowhere else on Linux.

Releases are cut by pushing a `v*` tag. No version is written down in the repo: `npm/stage.sh` stamps it into the binary with `-ldflags -X main.customVersion`, and `.github/workflows/release.yml` rehearses the publish against a throwaway registry before touching npm. `npm/rehearse.sh` is the only coverage for the npm JavaScript and shell, which is otherwise exercised only while cutting a release, the worst moment to learn it is broken. Same lesson as the `--service` flag that sat removed from the code and still passed in a workflow for two commits.

## Layout

`cmd/grove` is the cobra CLI, one file per command. Beyond `exec`, `ls`, `hold`, and the `start`/`stop`/`restart` trio, that is `install`/`uninstall` (the privileged and trust-store path), `doctor` (one finding plus a remedy per check, and usually where a platform change needs its matching edit), `init`, `env`, `context`, and `release`. `internal/*` holds everything with logic in it:

| | |
|-|-|
| `identity` | a directory to a context: project, variant, slug, hostname |
| `config` | `grove.toml`, `grove.local.toml`, and the `{token}` templates |
| `lease` | port allocation, and what a lease's lifetime is |
| `daemon` | the control protocol, the server, and where state lives |
| `proxy` | hostname to loopback port, with the 503 pages |
| `ca` / `trust` | the local root, the wildcard leaf, and the trust stores |
| `platform` | the only place GOOS branches; keeps build tags out of the rest |
| `redirect` | the macOS pf rules, as text, so Linux can test them |
| `shell` | quoting, for commands grove prints for a human to paste |

`npm/` is the published package: `cli/bin/grove.js` is the shim that picks a platform binary, `platform/` is the template the four of them are stamped from, and `stage.sh` and `rehearse.sh` build and rehearse a release. It is JavaScript and shell, so no Go test reaches it.

## How the pieces fit

**One daemon serves the machine.** `grove daemon` is hidden and runs in the foreground; `grove start`, `restart`, and any `grove exec` spawn it detached by re-executing `os.Executable()` with `Setsid`, logging to `daemon.log` in the state directory. Nothing supervises it and nothing starts it at boot, which on Linux is what lets `grove stop` hand 443 back to lando. On macOS it does not: the pf rule is machine wide, nothing in grove removes it, and while it is loaded a lando proxy bound to 443 is never reached.

**The control protocol is one JSON request per unix-socket connection.** `daemon.Version` is checked both on the way in and on the way back, and must be bumped when the wire shape changes: a binary is rebuilt far more often than its daemon is restarted, so the mismatch has to name itself rather than surface as a missing field.

**Lease lifetime is the point of the design.** An attached lease lasts exactly as long as the `acquire` connection, which the server holds open with `io.Copy(io.Discard, conn)` so the kernel reports the client exiting, crashing, or being killed. A detached lease outlives its command, and `grove hold` re-asserts a context's detached entries by reading the project again, which is what makes it safe for a worktree this process was never in. No lease survives a restart, so there is no lease state to garbage collect.

**Ports are derived, not stored.** An FNV hash of slug plus entry name lands inside 20000-20999, below Linux's ephemeral range, and collisions walk to the next free port. Only a walked *detached* port is written down, in `ports.json` beside the CA: without that record colliding entries re-resolve in whatever order they ask next restart, and an entry can be handed the port another worktree's stack is listening on. A database URL pointing at someone else's data is why the file exists.

**Environment layering has a deliberate order** (`layer` in `cmd/grove/exec.go`): trust variables, then `env_files` (which yield to anything already in the caller's environment), then `GROVE_CONTEXT` and friends, then grove's resolved values last. Grove's own win, because a route naming a port it did not lease would be a lie.

**Templates are validated at load.** `checkTemplates` resolves every `{token}` against dummy bindings when the config is read, so a typo fails at load rather than at use. Unknown TOML keys are an error too, which is why `schema` exists in the file format before anything writes it.

**Low ports differ by platform.** Linux lowers `ip_unprivileged_port_start` and binds 443 directly; macOS cannot lower anything, so the daemon binds 10443/10080 and pf redirects. Either way grove *prints* the privileged step rather than running it, and `redirect` is pure string manipulation so its tests run anywhere.

**What install prints is tested by pasting it.** The `macos-443` job greps `^[[:space:]]+sudo ` out of `grove install`'s output, runs those lines, and asserts `pf sends 443 to`. So the two-space indent in `platform_*.go` and that phrasing in `internal/redirect` are load-bearing with no Go test covering either: a job reconstructing the instructions could pass while the real ones were wrong. It points `GROVE_STATE_DIR` at a path containing a space on purpose, because splitting those paths into the wrong number of arguments once passed CI without one.

`grov.site` is real wildcard DNS into loopback, so there are no hosts-file entries to manage. The domain is not a config key yet: `defaultDomain` in `cmd/grove/main.go` is a constant, and nothing in `internal/config` reads a domain.

## Conventions in this codebase

- **Comments say why, not what.** Nearly every non-obvious line carries the reason it is that way, often naming the bug that made it necessary. Match that density. Do not add comments that restate the code.
- **Errors name the fix.** Messages are written for someone who has never heard of grove, and they spell the command in the form the caller can actually run: `invocation()` returns `pnpm grove` or `npx grove` when the binary is inside `node_modules`, and `grove` otherwise.
- **Nothing is said in colour alone.** Output gets piped, `eval`'d, and captured by tests, so anything coloured also says the same thing in words. The palette in `cmd/grove/color.go` goes plain for a non-terminal, `NO_COLOR`, or `TERM=dumb`.
- **A tabwriter measures escape codes as width.** `grove ls` lays the table out plain and paints into the finished string afterwards. Keep that order.
- **Getting out of the way is a feature.** Under `CI` with no daemon answering, `grove exec` runs the command untouched; a build service is the authority on its own environment. `--if-available` asks for the same anywhere.
- **Exit codes are contracts.** A bad invocation exits 2 via `usageArgs`; a child's exit code and signal death pass through via `exitError`.

Escape hatches used by tests and by machines with awkward paths: `GROVE_CONTEXT_OVERRIDE`, `GROVE_STATE_DIR`, `GROVE_SOCKET`, `GROVE_LISTEN`, `GROVE_HTTP_LISTEN`, `GROVE_SYSTEM_BUNDLE`.

They are also how to try a change without disturbing the daemon serving someone's real worktrees: point `GROVE_STATE_DIR` and `GROVE_SOCKET` into a temp directory and `GROVE_LISTEN`/`GROVE_HTTP_LISTEN` at high ports, and a second daemon runs beside the live one. `grove restart` with the defaults stops the daemon that is in use.

`GROVE_BINARY_OVERRIDE` is the other half of that, and it is read by the npm shim rather than by Go: it names a binary to run instead of the one npm installed, so a consuming project's `pnpm dev` can exercise a working tree. The daemon inherits it, since grove re-executes itself. Note that turborepo's default strict env mode deletes any variable not in `globalPassThroughEnv`, so a task run through turbo will not see it unless the project lists it.

## Testing notes

Tests are plain `testing` with local `t.Helper()` builders, no assertion library. Two traps worth knowing:

- A unix socket path caps out near 104 bytes on macOS, and `t.TempDir()` there returns a `/var/folders/...` path long enough to blow it on its own. Tests that need a socket use a short `os.MkdirTemp("/tmp", "grove")` instead (see `socketDir` in `cmd/grove/exec_test.go`).
- `startDaemon` in the same file is the pattern for an in-process daemon: a fresh CA in a temp dir, listeners on port 0, and a `t.Cleanup` that shuts down and asserts `Serve` returned cleanly.

Commit messages are a short declarative subject saying what the change does for the reader, a blank line, then `- ` bullets giving the reasons. `git log` is the reference; recent subjects read "Say why the URLs in ls will not open" and "Fail as soon as the daemon does".
