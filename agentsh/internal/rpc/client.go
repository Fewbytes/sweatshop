package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"time"
)

type Client struct {
	Socket  string
	Timeout time.Duration
}

func (c Client) Call(ctx context.Context, request Request, result any) error {
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "unix", c.Socket)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

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
