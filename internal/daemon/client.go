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

	// Kept so Stop can ask again on a new connection: a daemon that refuses a
	// request answers once and hangs up.
	socket string
}

// Nothing usable answered, whether the socket was missing or left by a corpse.
type NotRunningError struct {
	Socket string
	Err    error
}

func (e *NotRunningError) Error() string {
	return fmt.Sprintf("grove is not running at %s; start it with 'grove start'", e.Socket)
}

func (e *NotRunningError) Unwrap() error { return e.Err }

func Dial(socket string) (*Client, error) {
	conn, err := net.Dial("unix", socket)
	if err != nil {
		return nil, &NotRunningError{Socket: socket, Err: err}
	}
	return &Client{conn: conn, dec: json.NewDecoder(conn), enc: json.NewEncoder(conn), socket: socket}, nil
}

func (c *Client) Close() error { return c.conn.Close() }

// Attached leases last until the client is closed, so the caller holds it open
// as long as the process it started is alive. Detached ones outlive it.
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

// Resolve asks what Acquire would hand out, and takes nothing. Unlike Acquire
// there is no lease riding on this connection, so closing it costs nothing.
func (c *Client) Resolve(slug, worktree string, entries []Entry) (map[string]Grant, error) {
	resp, err := c.roundTrip(Request{Op: OpResolve, Slug: slug, Worktree: worktree, Entries: entries})
	if err != nil {
		return nil, err
	}
	if resp.Grants == nil {
		return nil, errors.New("daemon: resolve returned no grants")
	}
	return resp.Grants, nil
}

// ListAnyVersion reads the table even from a daemon speaking an older protocol,
// which List is right to refuse. Safe here because of what the caller does
// with it: an older payload decodes into this struct with whatever it does not
// carry left zero, so a field that moved between versions costs entries rather
// than meaning them wrongly, and a restore that puts back fewer projects is
// what already happens when the table cannot be read at all.
func (c *Client) ListAnyVersion() ([]Live, error) {
	resp, err := c.exchange(Request{Op: OpList, Version: Version})
	if err != nil {
		return nil, err
	}
	if resp.Error == "" {
		return resp.Leases, nil
	}
	if resp.Version == 0 || resp.Version >= Version {
		return nil, errors.New(resp.Error)
	}

	older, err := Dial(c.socket)
	if err != nil {
		return nil, err
	}
	defer older.Close()

	again, err := older.exchange(Request{Op: OpList, Version: resp.Version})
	if err != nil {
		return nil, err
	}
	if again.Error != "" {
		return nil, errors.New(again.Error)
	}
	return again.Leases, nil
}

func (c *Client) List() ([]Live, error) {
	resp, err := c.roundTrip(Request{Op: OpList})
	if err != nil {
		return nil, err
	}
	return resp.Leases, nil
}

// Empty names releases all of them. Reports which were there to end.
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

// Detached leases go too: nothing survives the process.
//
// A daemon refusing this because it speaks an older protocol is asked again in
// that protocol. Upgrading grove is exactly how a mismatch arrives, and the old
// daemon predates any agreement to exempt stopping from its own version check,
// so without this every upgrade would end at killing a process by hand. Safe
// because a stop has only ever carried a version, a pid and an op.
func (c *Client) Stop() error {
	resp, err := c.exchange(Request{Op: OpStop, Version: Version})
	if err != nil {
		return err
	}
	if resp.Error == "" {
		// It stopped. That it replied in its own version is not this caller's
		// business, and comparing them here would report a failure that is not
		// one.
		return nil
	}
	if resp.Version == 0 || resp.Version >= Version {
		return errors.New(resp.Error)
	}

	older, err := Dial(c.socket)
	if err != nil {
		// Gone between the refusal and now, which is the outcome anyway.
		return nil
	}
	defer older.Close()

	again, err := older.exchange(Request{Op: OpStop, Version: resp.Version})
	if err != nil {
		return err
	}
	if again.Error != "" {
		return errors.New(again.Error)
	}
	return nil
}

// The wire without the reading of it, for a caller that needs the reply a
// refusal carries rather than only the fact of one.
func (c *Client) exchange(req Request) (Response, error) {
	req.PID = os.Getpid()
	if err := c.enc.Encode(req); err != nil {
		return Response{}, err
	}
	var resp Response
	if err := c.dec.Decode(&resp); err != nil {
		return Response{}, err
	}
	return resp, nil
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
	// the case worth naming.
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
	return fmt.Sprintf("the running daemon speaks control protocol v%d and this grove speaks v%d; restart it with 'grove restart', then 'grove hold' in each project, since a restart drops every detached port", e.Daemon, e.CLI)
}
