package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/grove-sh/cli/internal/daemon"
)

func newDaemonCommand() *cobra.Command {
	var socket, listen, domain, caDir string

	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run the grove proxy and lease registry in the foreground",
		Long: `Run the grove daemon in the foreground.

The daemon terminates TLS for every context's hostname, routes each one to the
port it leased, and holds the leases. A lease lasts exactly as long as the
'grove exec' connection that asked for it.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			server, err := daemon.New(daemon.Config{Domain: domain, CADir: caDir})
			if err != nil {
				return err
			}

			control, err := daemon.Listen(socket)
			if err != nil {
				return err
			}
			defer os.Remove(socket)

			https, err := net.Listen("tcp", listen)
			if err != nil {
				return bindHint(listen, err)
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			go func() {
				<-ctx.Done()
				server.Shutdown()
			}()

			fmt.Fprintf(cmd.OutOrStdout(), "grove daemon on %s, control socket %s\n", listen, socket)
			return server.Serve(control, https)
		},
	}

	cmd.Flags().StringVar(&socket, "socket", daemon.DefaultSocket(), "control socket path")
	cmd.Flags().StringVar(&listen, "listen", "127.0.0.1:443", "address to serve HTTPS on")
	cmd.Flags().StringVar(&domain, "domain", defaultDomain, "domain every context lives under")
	cmd.Flags().StringVar(&caDir, "ca-dir", daemon.StateDir(), "directory holding the local CA")
	return cmd
}

func bindHint(address string, err error) error {
	switch {
	case errors.Is(err, syscall.EACCES):
		return fmt.Errorf("%w\nBinding a port below 1024 needs privileges grove does not have yet. Try --listen 127.0.0.1:8443", err)
	case errors.Is(err, syscall.EADDRINUSE):
		return fmt.Errorf("%w\nSomething already holds %s. If you use lando, 'lando poweroff' releases it, or try --listen 127.0.0.1:8443", err, address)
	}
	return err
}
