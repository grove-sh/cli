<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="./docs/assets/grove-banner-dark.svg">
    <img src="./docs/assets/grove-banner-light.svg" alt="grove" width="300">
  </picture>
</p>

<p align="center">
  Local HTTPS hostnames, port allocation, and env vars, scoped per git worktree.
</p>

<p align="center">
  <a href="https://www.npmjs.com/package/@grove-sh/cli"><img alt="npm" src="https://img.shields.io/npm/v/@grove-sh/cli?style=flat-square&color=3C782C"></a>
  <a href="https://github.com/grove-sh/cli/actions/workflows/ci.yml"><img alt="Build" src="https://img.shields.io/github/actions/workflow/status/grove-sh/cli/ci.yml?branch=main&label=build&style=flat-square&color=3C782C"></a>
  <a href="https://github.com/grove-sh/cli/blob/main/LICENSE"><img alt="License" src="https://img.shields.io/badge/license-Apache--2.0-3C782C?style=flat-square"></a>
</p>

---

You probably have a feature you're working on, a PR you promised to review, and an agent or two grinding away on branches of their own, sometimes in a different repo. Nothing stops you running all of that at once. Nothing coordinates it either. You pick ports by hand, you edit `.env` in each checkout, and you swear you'll put it all back afterwards. So mostly you stop one thing to look at another.

Grove uses worktrees to keep contexts separate. Every worktree of every repo on your machine is a context, and each one gets its own hostname, its own ports, and its own env vars. One daemon serves them all, so run as many worktrees on as many projects as you like and they won't collide:

```
~/code/app            https://app.grov.site        web on 20107   postgres on 20402
~/worktrees/app/feat  https://app-feat.grov.site   web on 20871   postgres on 20533
~/code/shop           https://shop.grov.site       web on 20334   postgres on 20118
```

No port in the URL, a certificate your browser actually trusts, and one command to run anything inside that context:

```sh
grove exec -- pnpm dev
```

There's nothing to configure per worktree. Ports come from a hash of the worktree and the thing being served, so a fresh checkout is addressable the moment it exists and leaves nothing behind when you delete it. And since the worktree's name is a value you can template, `{context.slug}` in a database URL points each one at its own database on the postgres you're already running.

## Quickstart

```sh
npm install -g @grove-sh/cli
grove install            # once per machine
grove init               # once per project, writes grove.toml
grove exec -- pnpm dev
```

## Why not lando or ddev?

You could describe grove as "lando, but bring your own services". That's close, but it undersells the part that matters.

lando and ddev get a lot right. You get a real hostname like `app.lndo.site` or `app.ddev.site`, https with a certificate your browser trusts, and a stack that comes up with one command. Grove keeps all of that.

What they assume, along with docker compose, is that the project is the unit. One checkout, one hostname, one set of ports. Clone the same project a second time and it collides with the first until you rename it and hand-edit the config. Their answer is to wrap everything in containers, which does isolate your services. Unfortunately it isolates your tooling right along with them, and you're still stuck at one checkout per project.

Grove takes a different approach.

**A context is a worktree.** Hostnames, ports, and env vars are derived from the worktree, not written down. A fresh checkout serves on its own name with no setup, and five checkouts are no harder than one. That holds across repos too, so nothing defaults to 3000 twice. Run several agents at once and each has its own stack to check its work against.

**Bring your own services and namespace them.** Grove doesn't run postgres or redis. It addresses the ones you already run and gives each worktree its own slice. `{context.slug}` in a connection string is a database per worktree on one server. The service is shared; the data isn't.

**Your tooling stays on your host.** `pnpm dev` runs as you, with your node and your files. Hot reload watches the real filesystem, your debugger attaches, and the paths in your editor are the paths the process sees. No VM to feed, no bind mount to wait on.

**Real hostnames, real HTTPS.** A wildcard certificate from a root your machine trusts, and real DNS under `grov.site`. No hosts file, no port in the URL, and secure cookies and OAuth redirects behave like production. Two branches are two origins, so they never share a session. `localhost:3000` and `localhost:3001` do, because cookies don't care about ports.

**Adopting it isn't a team decision.** Under CI grove steps aside and runs your command untouched, and `--if-available` asks for the same anywhere. A teammate who never ran `grove install` runs the same scripts. Your lando project keeps working too.

|                             | lando, ddev            | docker compose | grove               |
|-----------------------------|------------------------|----------------|---------------------|
| The unit                    | project                | project        | worktree            |
| Runs your services          | yes                    | yes            | no, addresses yours |
| Where your tooling runs     | container              | container      | your host           |
| Several checkouts at once   | rename and reconfigure | juggle ports   | nothing to do       |
| Needed in CI                | yes                    | yes            | no                  |

## What grove asks of your project

Your services have to be namespaceable. A database per worktree works because postgres has databases. A single-tenant listener like the Stripe webhook forwarder doesn't multiply, however nicely you ask. Each worktree's database also needs its own migrations and seed, which is a chore grove creates and does nothing about. Last, your dev server has to bind the port it's handed. Next.js reads `PORT` on its own, Vite has to be told, and a server that picks its own port is one grove can't reach. That catches nearly everyone, so it has [a section of its own](#your-dev-server-has-to-bind-the-port-its-given).

## Setting up a machine

```sh
npm install -g @grove-sh/cli
```

Or, if you've got Go handy, `go install github.com/grove-sh/cli/cmd/grove@latest`. Either way there's one more step, once per machine:

```sh
grove install
```

That generates a local certificate authority, adds it to your trust stores, and shows you the one privileged step your platform needs. On Linux that's a sysctl lowering the port floor. On macOS it's a pf redirect and a loopback alias, because nothing there can bind 443 as you. Those commands change your machine rather than your project, so grove prints them in full and asks before running them. `grove install --yes` answers in advance, and if there's no terminal to ask, the commands are printed and left to you.

Grove serves `127.0.0.4`, not `127.0.0.1`, so it sits beside whatever else wants port 443. The wildcard DNS points there, so you never see the address in a URL.

```
127.0.0.1:80    docker-proxy   <- lando
127.0.0.1:443   docker-proxy   <- lando
127.0.0.4:80    grove
127.0.0.4:443   grove
```

Nothing to stop, nothing to hand back, no `poweroff` before you can use something else.

Nothing runs at boot. Grove is up while you're using it. Any `grove exec` starts a daemon when none is answering, and `grove start` does it on its own. The exception is CI, where nothing answering means grove steps aside instead.

If a hostname won't open and you can't tell which half to blame, ask grove:

```sh
grove doctor
```

```
Grove           running
Authority       installed and trusted
Runtime bundle  /etc/ssl/certs/ca-certificates.crt, which carries grove's root
Version         v0.5.0
Platform        linux-x64
```

That's a healthy machine. Doctor also checks that the wildcard DNS resolves here and that 443 reaches grove, but those two only speak up when something is wrong, and when they do they name the fix. It's also the first thing to run after `grove install`, before anything has had a chance to go wrong.

## Setting up a project

```sh
grove init
```

This writes a `grove.toml` from what it finds: a route per app, and the ports a supabase stack would publish. Read it before you trust it. Grove is guessing which variable each app reads its URL from, and it's guessing from your dependencies.

```toml
# Loaded first, so anything below can override a line in it
env_files = [".env"]

[env]
POSTGRES_URL = "postgres://postgres@localhost:5432/{context.slug}"
NEXT_PUBLIC_SITE_URL = "{web.url}"
NEXT_PUBLIC_API_URL = "{api.url}"

# https://<context>.grov.site
[routes.web]
dir = "apps/web"
label = ""
env.PORT = "{port}"

# https://<context>-api.grov.site
[routes.api]
dir = "apps/api"
env.PORT = "{port}"
```

Each route gets a hostname under the context. A route's label is its own name unless you say otherwise, so `api` answers on `<context>-api.grov.site`, and an empty label puts the route on the bare context name.

`{port}` and `{url}` are this entry's own, so they only mean anything on an entry. `{api.url}` names another entry, whichever section it lives in, and `{context.slug}` is the worktree's name, which is how one shared database server gives every worktree its own database.

Where a variable sits decides when it's set. `[env]` is set for every command in the project. An env block on a route is only set while that route is bound, which is why a URL belongs in `[env]` even though it describes the route: a build needs it, and a build binds nothing. An env block on a **detached** entry is always set, exactly like `[env]`, so it reads as scoped while behaving as global. Put it in `[env]`, where the section already says so.

Then run your commands through it:

```sh
grove exec -- pnpm dev
grove exec -- psql "$POSTGRES_URL"
```

Grove loads your `env_files` too, so it replaces `dotenv-cli` rather than sitting beside it. Values from those files yield to your shell. Grove's own resolved values win over both, because a route pointing at a port it didn't lease would be a lie.

Want to bend one of those values for an afternoon? Put it in `.env.local` beside your `grove.toml`. That file is the top of the chain and yields to nothing, not even a variable you exported, and grove tells you on stderr whatever it took over. `GROVE_CONTEXT`, `GROVE_PORT`, `GROVE_HOST` and `GROVE_URL` are grove's own, so setting one there is an error rather than a lie about a lease. If your `env_files` already lists `.env.local`, it stays where you put it and grove leaves it in that lower tier.

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

One daemon serves every context, so `grove stop` drops every context's leases, not just this project's. It tells you what it dropped, and it refuses while anything is answering on a port grove leased, which `--force` overrides. `grove uninstall` stops grove too, since a root this machine no longer trusts leaves nothing worth serving.

On macOS, `grove uninstall` asks for authorization before it'll untrust the root, since removing a trust root isn't something to do quietly. Uninstall also offers to take back what install changed for port 443, the sysctl on Linux and the pf redirect on macOS, the same way: printed in full, run on a yes or with `--yes`.

## One app on more than one hostname

Some apps answer on two names. A storefront and its admin or a site and the host it serves assets from. That's still one server on one port, so in grove it's one route with an alias, not two routes.

```toml
# https://<context>.grov.site
# https://<context>-admin.grov.site
[routes.web]
dir = "apps/web"
label = ""
aliases = ["admin"]
env.PORT = "{port}"

[env]
SITE_URL = "{web.url}"
ADMIN_URL = "{web.admin.url}"
```

Both hostnames proxy to the one port grove leased for `web`, and both appear and disappear with that lease.

Templates tell the two apart. `{web.url}` is the route's own hostname, and so are `GROVE_HOST` and `GROVE_URL`, so a route's name always means one thing however many hostnames it answers on. The alias is `{web.admin.url}`, or `{web.admin.host}` for the bare name. There's no `{web.admin.port}`, because the alias shares the route's port.

An alias is a label under the domain like any other, which is why it's `<context>-admin.grov.site` and not `admin.<context>.grov.site`. And like any other label, only one route can claim it, whether as its own name or as an alias.

## Ports for things grove doesn't start

A route's lease normally lasts exactly as long as the command holding it. Run `grove exec -- pnpm dev`, and the port and hostname are yours until the dev server exits. That's the right shape for a process that's a child of grove, and the wrong shape for `supabase start` or `docker compose up -d`, which return in seconds and leave a stack holding ports for hours.

Mark those entries `detached`:

```toml
[env]
SUPABASE_PROJECT_ID = "{context.slug}"
SUPABASE_API_PORT = "{api.port}"
SUPABASE_DB_PORT = "{db.port}"

# https://<context>-api.grov.site
[routes.api]
detached = true

[ports.db]
detached = true
```

Two things are new here. A `[ports.x]` entry is a port with no hostname, for a database or an SMTP sink that nothing browses to. And `detached = true` means the lease outlives whatever command took it. Every `grove exec` in the project takes all of its detached entries, so the first command you run has already reserved the whole stack's ports, and the stack can come up on them and stay up after that command is gone.

```sh
grove exec -- supabase start
```

That gives supabase a project ID named after the worktree and ports that belong to this context, so two worktrees run two stacks side by side. `grove ls` shows them as `claimed` until something answers, and `running` once it does.

Detached ports also get written down. If two contexts collide on one and grove walks to the next free port, the walked port is recorded, so a restart hands the same port back and a database URL never quietly starts pointing at another worktree's data.

Since nothing exits to end a detached lease, you end it yourself. `grove release` hands back every detached port this context holds, or name the ones to let go: `grove release db`. And after a daemon restart, `grove hold` re-registers them, so a stack that's still running gets its hostname back.

## Your dev server has to bind the port it's given

Grove leases a port and proxies the hostname to it. So you run `grove exec -- pnpm dev`, the server announces itself on a port of its own choosing, and you get a 503 from something that looks like it's running fine. A server that picks its own port is a server grove can't reach.

Next.js reads `PORT` on its own. Vite doesn't, and its preview server is configured separately from its dev server:

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

`allowedHosts` matters as much as the port. Dev servers reject requests carrying a `Host` they don't recognise, and grove's whole point is a hostname they've never seen.

## When something doesn't answer

`grove ls` shows what this context has and what's holding it:

```
ROUTE  URL                    PORT   STATE
web    https://app.grov.site  20107  running
db     -                      20402  claimed
```

On a terminal, grove colors each URL by how much to believe it. Green opens. Yellow would open if something were listening. Red won't open until grove is finished being set up, and the line under the table says what's missing.

`idle` means nothing holds it. `claimed` means grove handed the port out and nothing answers on it, which is what a stopped stack looks like. `running` means something is really there.

A 503 tells you which half is wrong. "No grove context is bound to this hostname" means there's no lease, usually because the command that held it exited. "Nothing is listening on the port grove leased" means the lease is fine and your server is somewhere else, which is nearly always the port question above.

## Reading the ports without taking them

A route belongs to one command at a time. Start `grove exec -- pnpm dev` in a worktree, then try it again in a second terminal, and the second one is refused: the route is already running. That's the right answer for a second dev server, which would have nothing to bind. It's the wrong answer for a test suite, a smoke check, or a script that only wants to know where the dev server is.

Those get `--no-bind`:

```sh
grove exec --no-bind -- pnpm playwright test
```

It takes no lease, so it runs alongside the command holding the route, and it reports the port that command is actually on. Where nothing holds a port, it reports the one a lease would get, so your script still has a number to work with, but no hostname routes there until something takes the lease for real.

`grove env` is the same idea without a command. It prints the environment grove would hand you, from the leases that exist right now, and names on stderr any variable it had to leave out because nobody holds that port yet.

## How it decides things

A context is a worktree. Its name comes from the directory, or from `name` in `grove.toml`, and `GROVE_CONTEXT_OVERRIDE` replaces it outright.

A linked worktree appends its directory to that name and the main clone doesn't, which is what keeps `app` and `app-feat` apart. A bare repository has no main clone to be the project itself, so the worktree on the default branch is, and the others keep their suffix. Grove reads `origin/HEAD` for that, falling back to the repository's own `HEAD`, and `grove doctor` tells you which one it went by. Both are guesses, and the branch a repository defaults to is often not the one you work in, so name the worktree that is the project:

```sh
git config grove.mainWorktree dev
```

That lives in the repository's config, which every worktree reads and no branch carries, so there's one answer and two worktrees can't both claim it. A repository with a main clone ignores it, since the main clone is always the context that is the project.

Ports come from a hash of the context and the entry, so they're stable without being stored. When two contexts collide on one, grove walks to the next free port and writes that down.

Leases live in the daemon's memory. An attached lease lasts as long as the command that took it. A detached one outlives it, because something like `supabase start` returns in seconds and holds its ports for hours. Either way, stop the daemon and they're gone.

`grove restart` covers for that. Before it stops anything, it notes which worktrees hold detached leases. Once the new daemon is up, it runs the equivalent of `grove hold` in each of them. The ports come out the same as before, because they're derived from the context, so a running stack never notices.

Attached leases aren't restored. The dev server holding one keeps running on its port, but its hostname routes nowhere until you start it again under `grove exec`. Restart tells you which routes that cost.

Run `grove hold` yourself when restart didn't get the chance: after a crash, a reboot, or a `grove stop` followed by a `grove start` later.

On macOS two things need to survive a reboot: the pf rules, and `127.0.0.4` itself, since macOS only configures `127.0.0.1` on the loopback interface. So `grove install` stages a small root-owned launchd job that puts both back. That's the address and the firewall rule coming back, not grove.

Under CI, with no daemon answering, `grove exec` runs your command untouched. A build service is the authority on its own environment.

## When two groves disagree about this worktree

A context is derived from the worktree rather than written down, so two versions of grove can decide differently about the same directory. Ports come from the context, so a worktree that changes name changes every port with it, and a stack that's still running carries on listening on the old ones. Your stack is plainly up, `grove ls` shows ports nothing answers on, and nothing about that looks like a version problem.

`grove doctor` names both halves of it.

```
Project grove   0.4.3 in packages/db; 0.4.5 in this project
Context         holding ports as app-main, and resolving app now
```

`Project grove` reads what the project installs. A workspace can resolve a different copy per package, and the one that decides is under the package whose script starts the stack, not the one you type. Two versions there is worth fixing whatever else is true. One version that merely isn't the one you're running is reported without a warning, since it only matters where the two derive a context differently.

`Context` is the disagreement itself, read from the daemon: a worktree holding ports under one name while resolving another. That's the version split having already happened.

## Building

```sh
go build ./cmd/grove
go install ./cmd/grove
go test ./...
```
