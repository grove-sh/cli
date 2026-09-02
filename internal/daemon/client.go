package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
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
	return fmt.Sprintf("no grove daemon at %s; start one with 'grove daemon'", e.Socket)
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

// Acquire leases a port. The lease lasts until the client is closed, so the
// caller keeps it open for as long as the process it starts is alive.
func (c *Client) Acquire(slug, service, worktree string) (Grant, error) {
	resp, err := c.roundTrip(Request{Op: OpAcquire, Slug: slug, Service: service, Worktree: worktree})
	if err != nil {
		return Grant{}, err
	}
	if resp.Grant == nil {
		return Grant{}, errors.New("daemon: acquire returned no grant")
	}
	return *resp.Grant, nil
}

func (c *Client) List() ([]Entry, error) {
	resp, err := c.roundTrip(Request{Op: OpList})
	if err != nil {
		return nil, err
	}
	return resp.Leases, nil
}

func (c *Client) roundTrip(req Request) (Response, error) {
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
	return resp, nil
}
