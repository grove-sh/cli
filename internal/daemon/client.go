package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
)

type Client struct {
	conn net.Conn
	dec  *json.Decoder
	enc  *json.Encoder
}

// NotRunningError says nothing usable answered on the socket, whether the file
// was missing or a dead daemon left it behind.
type NotRunningError struct {
	Socket string
	Err    error
}

func (e *NotRunningError) Error() string {
	return fmt.Sprintf("no grove daemon at %s; start one with 'grove restart'", e.Socket)
}

func (e *NotRunningError) Unwrap() error { return e.Err }

func Dial(socket string) (*Client, error) {
	conn, err := net.Dial("unix", socket)
	if err != nil {
		return nil, &NotRunningError{Socket: socket, Err: err}
	}
	return &Client{conn: conn, dec: json.NewDecoder(conn), enc: json.NewEncoder(conn)}, nil
}

func (c *Client) Close() error { return c.conn.Close() }

// Acquire leases a port for each entry, keyed by name in the reply. Attached
// leases last until the client is closed, so the caller keeps it open for as
// long as the process it starts is alive; detached ones outlive it.
func (c *Client) Acquire(slug, worktree string, entries []Entry) (map[string]Grant, error) {
	resp, err := c.roundTrip(Request{Op: OpAcquire, Slug: slug, Worktree: worktree, Entries: entries})
	if err != nil {
		return nil, err
	}
	if resp.Grants == nil {
		return nil, errors.New("daemon: acquire returned no grants")
	}
	return resp.Grants, nil
}

func (c *Client) List() ([]Live, error) {
	resp, err := c.roundTrip(Request{Op: OpList})
	if err != nil {
		return nil, err
	}
	return resp.Leases, nil
}

// Release ends detached leases for a context, all of them when names is empty,
// and reports which were there to end.
func (c *Client) Release(slug, worktree string, names []string) ([]string, error) {
	resp, err := c.roundTrip(Request{Op: OpRelease, Slug: slug, Worktree: worktree, Names: names})
	if err != nil {
		return nil, err
	}
	return resp.Released, nil
}

func (c *Client) Status() (Status, error) {
	resp, err := c.roundTrip(Request{Op: OpStatus})
	if err != nil {
		return Status{}, err
	}
	if resp.Status == nil {
		return Status{}, errors.New("daemon: status came back empty")
	}
	return *resp.Status, nil
}

// Stop asks the daemon to shut down. Every attached lease ends with it, and
// detached ones go too, since nothing survives the process.
func (c *Client) Stop() error {
	_, err := c.roundTrip(Request{Op: OpStop})
	return err
}

func (c *Client) roundTrip(req Request) (Response, error) {
	req.Version = Version
	req.PID = os.Getpid()
	if err := c.enc.Encode(req); err != nil {
		return Response{}, err
	}
	var resp Response
	if err := c.dec.Decode(&resp); err != nil {
		return Response{}, err
	}
	if resp.Error != "" {
		return Response{}, errors.New(resp.Error)
	}
	// A daemon too old to know about versions reports none, which is exactly
	// the case worth naming: it predates this binary.
	if resp.Version != Version {
		return Response{}, &VersionError{Daemon: resp.Version, CLI: Version}
	}
	return resp, nil
}

// VersionError says the running daemon does not speak this binary's protocol.
type VersionError struct {
	Daemon int
	CLI    int
}

func (e *VersionError) Error() string {
	return fmt.Sprintf("the running daemon speaks control protocol v%d and this grove speaks v%d; restart it with 'grove restart', then 'grove sync' in each project, since a restart drops every detached port", e.Daemon, e.CLI)
}
