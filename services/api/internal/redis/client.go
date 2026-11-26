package redis

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	addr     string
	password string
	useTLS   bool
}

func NewClient(rawURL string) (*Client, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid redis url: %w", err)
	}

	pass, _ := u.User.Password()
	host := u.Host
	if !strings.Contains(host, ":") {
		host += ":6379"
	}

	return &Client{
		addr:     host,
		password: pass,
		useTLS:   u.Scheme == "rediss",
	}, nil
}

// Ping checks Redis connectivity with a PING command.
func (c *Client) Ping(ctx context.Context) error {
	conn, err := c.dialContext(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	reader := bufio.NewReader(conn)

	if c.password != "" {
		if err := sendCommand(conn, "AUTH", c.password); err != nil {
			return err
		}
		if _, err := reader.ReadString('\n'); err != nil {
			return err
		}
	}

	if err := sendCommand(conn, "PING"); err != nil {
		return err
	}
	resp, err := reader.ReadString('\n')
	if err != nil {
		return err
	}
	if !strings.HasPrefix(resp, "+PONG") {
		return fmt.Errorf("unexpected redis ping response: %s", strings.TrimSpace(resp))
	}
	return nil
}

// AcquireCooldown runs atomic SET <key> "1" NX EX <ttlSeconds>.
// Returns true if the lock was acquired (first alert), false if already in cooldown.
func (c *Client) AcquireCooldown(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	conn, err := c.dialContext(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

	reader := bufio.NewReader(conn)

	if c.password != "" {
		if err := sendCommand(conn, "AUTH", c.password); err != nil {
			return false, err
		}
		resp, err := reader.ReadString('\n')
		if err != nil || !strings.HasPrefix(resp, "+OK") {
			return false, fmt.Errorf("redis auth failed: %s %w", strings.TrimSpace(resp), err)
		}
	}

	ttlSec := strconv.Itoa(int(ttl.Seconds()))
	if err := sendCommand(conn, "SET", key, "1", "NX", "EX", ttlSec); err != nil {
		return false, err
	}

	resp, err := reader.ReadString('\n')
	if err != nil {
		return false, err
	}

	if strings.HasPrefix(resp, "+OK") {
		return true, nil
	}
	if strings.HasPrefix(resp, "$-1") {
		return false, nil
	}
	return false, fmt.Errorf("unexpected redis response: %s", strings.TrimSpace(resp))
}

func (c *Client) dialContext(ctx context.Context) (net.Conn, error) {
	dialer := &net.Dialer{}
	if c.useTLS {
		return tls.DialWithDialer(dialer, "tcp", c.addr, &tls.Config{MinVersion: tls.VersionTLS12})
	}
	return dialer.DialContext(ctx, "tcp", c.addr)
}

func (c *Client) dial() (net.Conn, error) {
	return c.dialContext(context.Background())
}

func sendCommand(conn net.Conn, args ...string) error {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("*%d\r\n", len(args)))
	for _, arg := range args {
		b.WriteString(fmt.Sprintf("$%d\r\n%s\r\n", len(arg), arg))
	}
	_, err := conn.Write([]byte(b.String()))
	return err
}
