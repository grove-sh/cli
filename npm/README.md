# The npm packages

`npm install -g @grove-sh/cli` has to end with a working `grove` on the path, and grove is a Go binary. This directory is how.

Five packages go out per release. Four hold a binary and nothing else:

```
@grove-sh/cli-darwin-arm64    @grove-sh/cli-linux-arm64
@grove-sh/cli-darwin-x64      @grove-sh/cli-linux-x64
```

Each declares the `os` and `cpu` it is for, and `@grove-sh/cli` lists all four as `optionalDependencies` pinned to the exact version. npm installs the one that matches the machine and silently skips the rest, so an install costs one binary rather than four.

The wrapper's `bin` is `bin/grove.js`, which finds the platform package that installed and hands over to it. A global install links the bin of the package you named and not the bins of its dependencies, so there is no arrangement that leaves node out of the picture. That makes the shim responsible for things a bare binary would get for free, signals in particular, and `rehearse.sh` checks it does them.

There is no `postinstall`. Setting grove up means generating a certificate authority and adding it to your trust stores, which is `grove install`, run deliberately, once.

## Files

| | |
|-|-|
| `cli/` | the wrapper, published as `@grove-sh/cli` |
| `platform/` | the template all four platform packages are stamped from |
| `stage.sh` | builds the binaries and assembles all five, ready to publish |
| `rehearse.sh` | publishes them to a throwaway registry and installs them for real |

Versions are committed as `0.0.0` and stamped at staging time. Nothing tracked by git is rewritten to cut a release.

## Rehearsing

```sh
npm/rehearse.sh
```

Needs node and go. It builds all four platforms, starts a Verdaccio on a free port, publishes to it, installs globally into a temp prefix, and then checks the things that are only true if the packaging is right: the binary kept its executable bit through the registry, only one build installed, a signal to the shim reaches the command grove is running, and the daemon that grove starts detaches instead of dying with node. Everything lands in a temp directory that is removed on exit, and no part of it touches npmjs.com, your npm cache, or a grove already running.

Do this before publishing. A version on npm cannot be reused, and cannot be removed at all after 72 hours, so a packaging bug found afterwards is a bug you ship a new version around.
