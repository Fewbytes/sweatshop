package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"time"
)

// DefaultDialTimeout bounds establishing the daemon connection. Reaching a
// local unix socket is fast, so a slow dial means the daemon is not listening.
const DefaultDialTimeout = 5 * time.Second

type Client struct {
	Socket string

	// DialTimeout bounds the connection attempt. Zero uses DefaultDialTimeout.
	DialTimeout time.Duration

	// Timeout bounds the entire call and is consulted only when ctx carries no
	// deadline; zero leaves the call bounded by ctx alone.
	//
	// This must stay separate from DialTimeout. A supervised command may
	// legitimately run for minutes with the response outstanding, and folding
	// the two together caps every call at the dial timeout — which silently
	// orphans the invocation, since the daemon runs it to completion regardless.
	Timeout time.Duration
}

func (c Client) Call(ctx context.Context, request Request, result any) error {
	dialTimeout := c.DialTimeout
	if dialTimeout <= 0 {
		dialTimeout = DefaultDialTimeout
	}
	dialer := net.Dialer{Timeout: dialTimeout}
	conn, err := dialer.DialContext(ctx, "unix", c.Socket)
	if err != nil {
		return err
	}
	defer conn.Close()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else if c.Timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(c.Timeout))
	}
	// Cancelling ctx must unblock a read that is already in flight.
	defer context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })()

	request.Version = Version
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return err
	}
	var response Response
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		return err
	}
	if response.Version != Version {
		return errors.New("unsupported daemon protocol version")
	}
	if response.Error != nil {
		return response.Error
	}
	if result == nil || len(response.Result) == 0 {
		return nil
	}
	return json.Unmarshal(response.Result, result)
}
