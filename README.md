# grove

Local HTTPS hostnames, ports, and env vars, scoped per git worktree.

Early development. Nothing here is stable yet.

## Build

```sh
go build ./cmd/grove
go install ./cmd/grove
go test ./...
```

`grove -v` reports the module version when installed from a tag, and the commit it was built from otherwise. Go embeds both, so there is nothing to configure.
