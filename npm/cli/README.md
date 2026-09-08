# grove

Two branches of the same app cannot both be running, not without juggling ports by hand and editing `.env` for each one. So mostly you stop the first to look at the second.

Grove makes a git worktree the unit. Each one gets its own hostname, its own ports, and its own environment, so several checkouts serve at the same time:

```
~/work/app            https://app.grov.site          postgres on 20402
~/worktrees/app/feat  https://app-feat.grov.site     postgres on 20533
```

No port in the URL, a real certificate your browser trusts, and one command to run anything inside that context:

```sh
grove exec -- pnpm dev
```

## Installing

```sh
npm install -g @grove-sh/cli
grove install
```

This package is a wrapper. It installs the binary for your platform and runs it.

`grove install` is a separate step on purpose. It generates a certificate authority and adds it to your trust stores, and it prints the one privileged step your platform needs instead of running it. Neither belongs in an `npm install` that nobody is watching.

## It runs beside your other tools

Grove serves `127.0.0.4`, so it never competes for port 443:

```
127.0.0.1:443   docker-proxy   <- lando
127.0.0.4:443   grove
```

Nothing to stop before you can use something else. And grove does not run your services, it addresses the ones you already start. If you want containers built and a stack managed, that is lando or ddev, and grove is happy to sit next to either.

Full documentation: https://github.com/grove-sh/cli

Early development. Nothing here is stable yet.
