# grove

Local HTTPS hostnames, ports, and env vars, scoped per git worktree.

Every worktree of a repository gets its own hostname, its own ports, and its own environment, so several checkouts run at the same time without colliding:

```
~/work/app            https://app.grov.site          postgres on 20402
~/worktrees/app/feat  https://app-feat.grov.site     postgres on 20533
```

No port in the URL, a real certificate your browser trusts, and one command to run anything inside that context:

```sh
grove exec -- pnpm dev
```

Early development. Nothing here is stable yet.

## Setting up a machine

Once per machine:

```sh
grove install
```

That generates a local certificate authority, adds it to your trust stores, and prints the one privileged step your platform needs. On Linux that is a sysctl lowering the port floor; on macOS it is a pf redirect, since nothing there can bind 443 as you. Grove prints those commands rather than running them, because they change the machine rather than your project.

It does not arrange for anything to run at boot. Grove is up while you are using it: `grove exec` starts a daemon when none is answering, and `grove start` does it on its own. Nothing lingers afterwards, which is what lets `grove stop` hand port 443 back to lando or anything else that wants it.

Then check it:

```sh
grove doctor
```

## Setting up a project

```sh
grove init
```

This writes a `grove.toml` from what it finds: a route per app, and the ports a supabase stack would publish. Read it before trusting it, since guessing which variable an app reads its URL from is exactly that.

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

This is the one thing that catches everybody. Grove leases a port and proxies the hostname to it, so a server that picks its own port is a server grove cannot reach, and you get a 503 from something that looks like it is running fine.

Next reads `PORT` on its own. Vite does not, and its preview server is configured separately from its dev server:

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

`allowedHosts` matters as much as the port: dev servers reject requests carrying a `Host` they do not recognise, and grove's whole point is a hostname they have never seen.

## When something does not answer

`grove ls` shows what this context has and what is holding it:

```
ROUTE   URL                        PORT   STATE    PID
web     https://app.grov.site      20107  running  514914
db      -                          20402  claimed  -
```

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

One daemon serves every context, so `grove stop` drops every context's leases
rather than just this project's. It says so when it does.

## How it decides things

A context is a worktree. Its name comes from the directory, or from `name` in `grove.toml`, and `GROVE_CONTEXT_OVERRIDE` replaces it outright.

Ports come from a hash of the context and the entry, so they are stable without being stored, and two contexts that collide on one are resolved by walking to the next free port and writing that down.

Leases live in the daemon's memory. An attached lease lasts as long as the command that took it; a detached one outlives it, because `supabase start` returns in seconds and holds its ports for hours. Nothing survives a daemon restart: the ports are derived from the context so they come back the same, but the hostnames have nowhere to route until something says the context exists. `grove hold` is that something. `grove restart` does it for you, since it reads the table a moment before dropping it and then asks each of those projects what it wants, so a planned restart costs nothing. `hold` is for the times nothing had the chance: a crash, a reboot, or a stop and a later start.

On macOS the pf rules are the one thing that does need to survive a reboot, so `grove install` stages a small root-owned launchd job whose only work is reloading them. That is the firewall rule coming back, not grove.

Under CI, with no daemon answering, `grove exec` runs your command untouched. A build service is the authority on its own environment.

## Building

```sh
go build ./cmd/grove
go install ./cmd/grove
go test ./...
```
