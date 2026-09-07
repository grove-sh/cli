# grove

Local HTTPS hostnames, ports, and env vars, scoped per git worktree.

Every worktree of a repository gets its own hostname, its own ports, and its own environment, so several checkouts run at the same time without colliding:

```
~/work/app            https://app.grov.site          postgres on 20402
~/worktrees/app/feat  https://app-feat.grov.site     postgres on 20533
```

```sh
npm install -g @grove-sh/cli
grove install
```

This package is a wrapper: it installs the binary for your platform and runs it. `grove install` is a separate step on purpose, because it generates a certificate authority and adds it to your trust stores, which is not something to do quietly during an `npm install`.

Full documentation: https://github.com/grove-sh/cli

Early development. Nothing here is stable yet.
