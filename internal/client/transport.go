package client

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"sync"
	"sync/atomic"

	"taskronin/internal/api"
)

type countingConn struct {
	net.Conn
	written atomic.Int64
}

func (c *countingConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.written.Add(int64(n))
	return n, err
}

func newHTTPClient(socket string) *http.Client {
	dialer := &net.Dialer{}
	return &http.Client{
		Transport: &http.Transport{
			// AIDEV-NOTE: net/http silently retries some requests on reused
			// connections (including requests with Idempotency-Key). Fresh
			// connections keep all retry counts/backoffs under our policy.
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				conn, err := dialer.DialContext(ctx, "unix", socket)
				if err != nil {
					return nil, err
				}
				return &countingConn{Conn: conn}, nil
			},
		},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// transmission snapshots the connection's byte count when this attempt gets it.
// GotConn can run on a transport goroutine, hence the lock as well as the counter.
type transmission struct {
	mu     sync.Mutex
	conn   *countingConn
	before int64
}

func (t *transmission) gotConn(info httptrace.GotConnInfo) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.conn = info.Conn.(*countingConn)
	t.before = t.conn.written.Load()
}

func (t *transmission) sent() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.conn != nil && t.conn.written.Load() > t.before
}

func (c *Client) attempt(ctx context.Context, k call, body []byte, key, generation string) (*http.Response, []byte, bool, error) {
	path := "/api/v1" + k.Path
	if k.Path == "/healthz" {
		path = k.Path
	}
	u := url.URL{Scheme: "http", Host: "taskronin", Path: path, RawQuery: k.Query.Encode()}
	tx := &transmission{}
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotConn: tx.gotConn})
	req, err := http.NewRequestWithContext(ctx, k.Method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, nil, false, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set(api.HeaderChannel, c.opts.Channel)
	if k.IfNoneMatch != "" {
		req.Header.Set("If-None-Match", k.IfNoneMatch)
	}
	if k.Kind != callRead {
		req.Header.Set(api.HeaderGeneration, generation)
		req.Header.Set("Content-Type", api.ContentTypeJSON)
	}
	if k.Kind == callKeyed {
		req.Header.Set(api.HeaderIdempotencyKey, key)
		if k.Mut.IfMatch != "" {
			req.Header.Set("If-Match", k.Mut.IfMatch)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, tx.sent(), err
	}
	defer func() { _ = resp.Body.Close() }()
	if k.Stream != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_, err := io.Copy(k.Stream, resp.Body)
		return resp, nil, tx.sent(), err
	}
	data, err := io.ReadAll(resp.Body)
	return resp, data, tx.sent(), err
}
