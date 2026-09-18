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
NEXT_PUBLIC_SITE_URL = "{web.url}"

[routes.web]
dir = "apps/web"
label = ""
env.PORT = "{port}"
```

`{port}` and `{url}` are this entry's own, so they only mean anything on an entry. `{db.port}` names another entry, whichever section it lives in, and `{context.slug}` is the worktree's name, which is how one shared database server gives every worktree its own database.

Where a variable sits decides when it is set. `[env]` is set for every command in the project. An env block on a route is set only while that route is bound, which is why a URL belongs in `[env]` even though it describes the route: a build needs it, and a build binds nothing. An env block on a **detached** entry is set always, exactly like `[env]`, so it reads as scoped while behaving as global, and belongs in `[env]` where the section already says so.

Then run your commands through it:

```sh
grove exec -- pnpm dev
grove exec -- psql "$POSTGRES_URL"
```

Grove loads your `env_files` too, so it replaces `dotenv-cli` rather than sitting beside it. Values from those files yield to your shell; grove's own resolved values win over both, since a route pointing at a port it did not lease would be a lie.

To bend one of those values for an afternoon, put it in `.env.local` beside your `grove.toml`. That file is the top of the chain and yields to nothing, not even a variable you exported, and grove names on stderr whatever it took over. `GROVE_CONTEXT`, `GROVE_PORT`, `GROVE_HOST` and `GROVE_URL` are grove's own, so setting one there is an error rather than a lie about a lease. If your `env_files` already lists `.env.local`, it stays where you put it and grove leaves it in that lower tier.

## One app on more than one hostname

Some apps answer on two names: a storefront and its admin, or a site and the host it serves assets from. That is one port, so it is one route with aliases rather than two routes.

```toml
[routes.web]
dir = "apps/web"
aliases = ["admin"]
env.PORT = "{port}"

[env]
SITE_URL = "{web.url}"
ADMIN_URL = "{web.admin.url}"
```

Every one of those hostnames proxies to the one port grove leased, and they come and go with the lease together. `{web.url}`, `GROVE_HOST` and `GROVE_URL` stay the route's own hostname, so a single name means one thing however many the route answers on; each alias is reachable as `{web.<alias>.url}` or `.host`. There is no `{web.admin.port}`, since an alias is another hostname on the same port and that port is `{web.port}`.

An alias is a label under the domain like every other, so it answers on `<context>-admin.grov.site` rather than as a subdomain of the route. No two routes can claim one label, whether either of them named it as its own or as an alias.

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

When every port grove handed out is claimed and nothing answers on any of them, `grove ls` says so under the table. One port answering keeps it quiet, because a stack comes up a service at a time. All of them silent means something larger: the stack is stopped, or it is running under a context grove is not looking at, which is the next section.

## When two groves disagree about this worktree

A context is derived from the worktree rather than written down, so two versions of grove can decide differently about the same directory. Ports come from the context, so a worktree that changes name changes every port with it, and a stack that is still running carries on listening on the old ones. Nothing about that looks like a version problem: `grove ls` shows ports nothing answers on, and the stack is plainly up.

`grove doctor` names both halves of it.

```
Project grove   0.4.3 in packages/db; 0.4.5 in this project
Context         holding ports as app-main, and resolving app now
```

`Project grove` reads what the project installs. A workspace can resolve a different copy per package, and the one that decides is under the package whose script starts the stack, not the one you type. Two versions there is worth fixing whatever else is true. One version that is merely not the one you are running is reported without a warning, since it only matters where the two derive a context differently.

`Context` is the disagreement itself, read from the daemon: a worktree holding ports under one name while resolving another. That is the version split having already happened.

## Commands

```
grove init                       write a grove.toml for this project
grove install                    set this machine up: authority, trust, port 443
grove uninstall                  stop grove and untrust its root
grove exec [-s <name>] -- <cmd>  run a command in this context
grove env [--format shell|json]  print that environment instead of running
grove ls [--all]                 this context's routes, or every lease on the machine
grove hold                       take this context's detached ports
grove release [name...]          let them go
grove context [--json]           how this directory resolves
grove doctor                     DNS, trust, the daemon, and port 443
grove start | stop | restart     the daemon, which is one process for the machine
```

One daemon serves every context, so `grove stop` drops every context's leases and not just this project's. It tells you what it dropped, and it refuses while anything is answering on a port grove leased, which `--force` overrides. `grove uninstall` stops grove too, since a root this machine no longer trusts leaves nothing worth serving.

A route belongs to one command at a time, which is right for the thing that listens on the port and wrong for a test suite that only reads it. `grove exec --no-bind` reports the ports and takes no lease, so it runs alongside the dev server holding them and reports the port that dev server is on. Where nothing holds a port it reports the one a lease would get, and no hostname routes there.

On macOS, `grove uninstall` asks for authorization before it will untrust the root, since removing a trust root is not something to do quietly. In a terminal with no way to show that prompt, over ssh for instance, it waits rather than failing.

## How it decides things

A context is a worktree. Its name comes from the directory, or from `name` in `grove.toml`, and `GROVE_CONTEXT_OVERRIDE` replaces it outright.

A linked worktree appends its directory to that name and the main clone does not, which is what keeps `app` and `app-feat` apart. A bare repository has no main clone to be the project itself, so the worktree on the default branch is, and the others keep their suffix. Grove reads `origin/HEAD` for that, falling back to the repository's own `HEAD`, and `grove doctor` says which of them it went by. Both are guesses, and the branch a repository defaults to is often not the one you work in, so name the worktree that is the project:

```sh
git config grove.mainWorktree dev
```

That lives in the repository's config, which every worktree reads and no branch carries, so there is one answer and two worktrees cannot both claim it. A repository with a main clone ignores it, since the main clone is always the context that is the project.

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
