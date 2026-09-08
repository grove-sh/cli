<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="./docs/assets/grove-banner-dark.svg">
    <img src="./docs/assets/grove-banner-light.svg" alt="grove" width="300">
  </picture>
</p>

<p align="center">
  Local HTTPS hostnames, ports, and env vars, scoped per git worktree.
</p>

<p align="center">
  <a href="https://www.npmjs.com/package/@grove-sh/cli"><img alt="npm" src="https://img.shields.io/npm/v/@grove-sh/cli?style=flat-square&color=3C782C"></a>
  <a href="https://github.com/grove-sh/cli/actions/workflows/ci.yml"><img alt="Build" src="https://img.shields.io/github/actions/workflow/status/grove-sh/cli/ci.yml?branch=main&label=build&style=flat-square&color=3C782C"></a>
  <a href="https://github.com/grove-sh/cli/blob/main/LICENSE"><img alt="License" src="https://img.shields.io/badge/license-Apache--2.0-3C782C?style=flat-square"></a>
</p>

---

Two branches of the same app cannot both be running, not without juggling ports by hand and editing `.env` for each one, and then forgetting to change them back. So mostly you stop the first thing to look at the second.

Grove makes a git worktree the unit. Every worktree of a repository gets its own hostname, its own ports, and its own environment, so several checkouts serve at the same time without colliding:

```
~/work/app            https://app.grov.site          postgres on 20402
~/worktrees/app/feat  https://app-feat.grov.site     postgres on 20533
```

No port in the URL, a real certificate your browser trusts, and one command to run anything inside that context:

```sh
grove exec -- pnpm dev
```

Nothing is configured per worktree. Ports come from a hash of the worktree and the thing being served, so a new checkout is addressable the moment it exists. And because the worktree's name is a value you can template, `{context.slug}` in a database URL points each one at its own database on the server you already run.

## It does not want your machine

Grove serves `127.0.0.4`, not `127.0.0.1`, so it runs beside whatever else wants port 443. The wildcard DNS points there, so the address never shows up in a URL.

```
127.0.0.1:80    docker-proxy   <- lando
127.0.0.1:443   docker-proxy   <- lando
127.0.0.4:80    grove
127.0.0.4:443   grove
```

Nothing to stop, nothing to hand back, no `poweroff` before you can use something else.

The rest follows the same instinct. Grove prints the one privileged step your platform needs instead of running it. It registers nothing to start at boot. It derives its ports instead of storing them, and under CI it steps aside and runs your command untouched. `grove uninstall` returns everything it borrowed.

## What it is not

Grove does not run your services. It addresses the ones you already start, whether that is `pnpm dev`, a supabase stack, or docker compose. If you want something to build containers and manage a stack, you want lando or ddev, and grove is happy to sit next to either.

Early development. Nothing here is stable yet.

## Setting up a machine

```sh
npm install -g @grove-sh/cli
```

Or, if you have Go: `go install github.com/grove-sh/cli/cmd/grove@latest`. Either way there is one more step, once per machine:

```sh
grove install
```

That generates a local certificate authority, adds it to your trust stores, and prints the one privileged step your platform needs. On Linux that is a sysctl lowering the port floor; on macOS it is a pf redirect and a loopback alias, since nothing there can bind 443 as you. Grove prints those commands rather than running them, because they change the machine rather than your project.

Nothing runs at boot. Grove is up while you are using it. Any `grove exec` starts a daemon when none is answering, and `grove start` does it on its own.

Then check it:

```sh
grove doctor
```

## Setting up a project

```sh
grove init
```

This writes a `grove.toml` from what it finds: a route per app, and the ports a supabase stack would publish. Read it before trusting it. Grove is guessing which variable each app reads its URL from, and it guesses from your dependencies.

```toml
[env]
POSTGRES_URL = "postgres://postgres@localhost:5432/{context.slug}"

[routes.web]
dir = "apps/web"
label = ""
env.PORT = "{port}"
env.NEXT_PUBLIC_SITE_URL = "{url}"
```

`{port}` and `{url}` are this entry's own. `{db.port}` names another entry, whichever section it lives in, and `{context.slug}` is the worktree's name, which is how one shared database server gives every worktree its own database.

Then run your commands through it:

```sh
grove exec -- pnpm dev
grove exec -- psql "$POSTGRES_URL"
```

Grove loads your `env_files` too, so it replaces `dotenv-cli` rather than sitting beside it. Values from those files yield to your shell; grove's own resolved values win over both, since a route pointing at a port it did not lease would be a lie.

## Your dev server has to bind the port it is given

Grove leases a port and proxies the hostname to it, so a server that picks its own port is a server grove cannot reach, and you get a 503 from something that looks like it is running fine.

Next.js reads `PORT` on its own. Vite does not, and you configure its preview server separately from its dev server:

```ts
// vite.config.ts
const port = Number(process.env.PORT) || undefined;
const site = process.env.VITE_SITE_URL;
const allowedHosts = site ? [new URL(site).hostname] : undefined;

export default defineConfig({
  server: { port, allowedHosts },
  preview: { port, allowedHosts },
});
```

`allowedHosts` matters as much as the port. Dev servers reject requests carrying a `Host` they do not recognise, and grove's whole point is a hostname they have never seen.

## When something does not answer

`grove ls` shows what this context has and what is holding it:

```
ROUTE  URL                    PORT   STATE
web    https://app.grov.site  20107  running
db     -                      20402  claimed
```

On a terminal, grove colours each URL by how much to believe it. Green opens. Yellow would open if something were listening. Red will not open until grove is finished being set up, and the line under the table says what is missing.

`idle` means nothing holds it, `claimed` means grove handed the port out and nothing answers on it, which is what a stopped stack looks like, and `running` means something is really there.

A 503 says which half is wrong. "No grove context is bound to this hostname" means there is no lease, usually because the command that held it exited. "Nothing is listening on the port grove leased" means the lease is fine and your server is somewhere else, which is nearly always the port question above.

## Commands

```
grove init                       write a grove.toml for this project
grove install                    set this machine up: authority, trust, port 443
grove uninstall                  remove grove's root from the trust stores
grove exec [-s <name>] -- <cmd>  run a command in this context
grove env [--format shell|json]  print that environment instead of running
grove ls [--all]                 this context's routes, or every lease on the machine
grove hold                       take this context's detached ports
grove release [name...]          let them go
grove context [--json]           how this directory resolves
grove doctor                     DNS, trust, the daemon, and port 443
grove start | stop | restart     the daemon, which is one process for the machine
```

One daemon serves every context, so `grove stop` drops every context's leases and not just this project's. It tells you what it dropped.

On macOS, `grove uninstall` asks for authorization before it will untrust the root, since removing a trust root is not something to do quietly. In a terminal with no way to show that prompt, over ssh for instance, it waits rather than failing.

## How it decides things

A context is a worktree. Its name comes from the directory, or from `name` in `grove.toml`, and `GROVE_CONTEXT_OVERRIDE` replaces it outright.

Ports come from a hash of the context and the entry, so they are stable without being stored. When two contexts collide on one, grove walks to the next free port and writes that down.

Leases live in the daemon's memory. An attached lease lasts as long as the command that took it. A detached one outlives it, because `supabase start` returns in seconds and holds its ports for hours.

No lease survives a daemon restart. The ports come back the same, since they are derived from the context, but the hostnames have nowhere to route until something says the context exists. `grove hold` is that something. `grove restart` does it for you, reading the table a moment before it drops it and then asking each of those projects what it wants. So a planned restart costs nothing, and `hold` is for the times nothing had the chance. A crash, a reboot, or a stop and a later start.

On macOS two things need to survive a reboot: the pf rules, and `127.0.0.4` itself, since macOS configures only `127.0.0.1` on the loopback interface. So `grove install` stages a small root-owned launchd job that puts back both. That is the address and the firewall rule coming back, not grove.

Under CI, with no daemon answering, `grove exec` runs your command untouched. A build service is the authority on its own environment.

## Building

```sh
go build ./cmd/grove
go install ./cmd/grove
go test ./...
```
