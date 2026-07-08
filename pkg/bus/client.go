package bus

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// ClientConfig configures a NATS client connection for peels.
type ClientConfig struct {
	// URLs is one or more NATS server URLs to connect to.
	// Example: ["nats://master-01:4222", "nats://master-02:4222"]
	URLs []string

	// Name identifies this client in server logs.
	Name string

	// TLS is the TLS configuration. Required in production.
	TLS *tls.Config

	// CAFile is the path to a PEM CA bundle used to verify the NATS server
	// certificate. Unlike baking a pool into TLS.RootCAs (frozen for the
	// process lifetime), the file is re-read on EVERY (re)connect attempt:
	// CA rotation is a file drop, and a file that does not exist yet
	// (provisioned out-of-band by the operator or init tooling) fails only
	// the current attempt — the reconnect loop heals once it appears.
	// Composes with TLS: the base config's floor/settings apply, the pool
	// comes from this file at connect time.
	CAFile string

	// CAFileOptional makes a missing CAFile mean "system trust store for
	// this attempt" instead of a failed attempt. Used for the conventional
	// <auth_dir>/nats-ca.crt candidate, which is consulted per attempt so a
	// CA dropped there later takes effect without a restart; explicit
	// operator-configured paths stay strict (never silently downgraded).
	CAFileOptional bool

	// CredsFile is the path to a NATS credentials file (JWT + nkey).
	CredsFile string

	// NKeySeedFile is the path to an nkey seed file for authentication.
	NKeySeedFile string

	// RetryConnect makes an unreachable NATS server at startup non-fatal:
	// NewClient returns a client in reconnecting state that keeps retrying
	// in the background (governed by MaxReconnects/ReconnectWait). Daemons
	// (master, peel, watchdog) set this so they boot while the control
	// plane is down; interactive tools (the operator CLI) leave it false
	// to fail fast with a clear connection error instead of delayed
	// request timeouts.
	RetryConnect bool

	// MaxReconnects is the maximum number of reconnection attempts. -1 = unlimited.
	// Defaults to -1.
	MaxReconnects int

	// ReconnectWait is the base wait time between reconnection attempts.
	// Defaults to 2 seconds. NATS adds jitter automatically.
	ReconnectWait time.Duration

	// ReconnectBufSize is the size of the internal buffer for messages
	// published during reconnection. Defaults to 8MB.
	ReconnectBufSize int

	// PingInterval is the interval for NATS ping/pong health checks.
	// Defaults to 20 seconds.
	PingInterval time.Duration

	// MaxPingsOut is the number of outstanding pings before declaring unhealthy.
	// Defaults to 3.
	MaxPingsOut int

	// DrainTimeout is the timeout for draining subscriptions during shutdown.
	// Defaults to 30 seconds.
	DrainTimeout time.Duration

	// Logger is the structured logger. Defaults to slog.Default().
	Logger *slog.Logger

	// OnReconnect, if set, is invoked whenever the client (re)connects to
	// NATS. With RetryConnect, this also fires when the initial connection
	// is established after a failed first attempt. Intended for metrics
	// wiring; must not block.
	OnReconnect func()

	// OnDisconnect, if set, is invoked with the disconnect error whenever
	// the client loses its NATS connection. Intended for metrics wiring;
	// must not block.
	OnDisconnect func(error)

	// OnSlowConsumer, if set, is invoked when NATS reports a slow-consumer
	// condition on one of this client's subscriptions. Intended for
	// metrics wiring; must not block.
	OnSlowConsumer func()
}

func (c *ClientConfig) defaults() {
	if c.Name == "" {
		c.Name = "zester-peel"
	}
	if c.MaxReconnects == 0 {
		c.MaxReconnects = -1 // unlimited
	}
	if c.ReconnectWait == 0 {
		c.ReconnectWait = 2 * time.Second
	}
	if c.ReconnectBufSize == 0 {
		c.ReconnectBufSize = 8 * 1024 * 1024 // 8MB
	}
	if c.PingInterval == 0 {
		c.PingInterval = 20 * time.Second
	}
	if c.MaxPingsOut == 0 {
		c.MaxPingsOut = 3
	}
	if c.DrainTimeout == 0 {
		c.DrainTimeout = 30 * time.Second
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
}

// Client manages a NATS client connection for peel nodes.
// It handles reconnection, health monitoring, and provides JetStream access.
type Client struct {
	nc     *nats.Conn
	js     jetstream.JetStream
	config ClientConfig
	logger *slog.Logger

	mu      sync.RWMutex
	healthy bool

	disconnectCh chan struct{}
	reconnectCh  chan struct{}
}

// NewClient creates a new NATS client connection for a peel.
// The connection is established immediately; use Shutdown to close.
func NewClient(cfg ClientConfig) (*Client, error) {
	cfg.defaults()

	if len(cfg.URLs) == 0 {
		return nil, fmt.Errorf("bus: at least one NATS URL is required")
	}

	c := &Client{
		config:       cfg,
		logger:       cfg.Logger,
		healthy:      false,
		disconnectCh: make(chan struct{}, 1),
		reconnectCh:  make(chan struct{}, 1),
	}

	opts := []nats.Option{
		nats.Name(cfg.Name),
		nats.MaxReconnects(cfg.MaxReconnects),
		nats.ReconnectWait(cfg.ReconnectWait),
		nats.ReconnectBufSize(cfg.ReconnectBufSize),
		nats.PingInterval(cfg.PingInterval),
		nats.MaxPingsOutstanding(cfg.MaxPingsOut),
		nats.DrainTimeout(cfg.DrainTimeout),
		nats.ReconnectJitter(500*time.Millisecond, 5*time.Second),
		// With RetryConnect, an unreachable server at startup is non-fatal:
		// NewClient returns a client in reconnecting state that keeps
		// retrying in the background (governed by MaxReconnects/
		// ReconnectWait above), so daemons can boot offline-first while the
		// control plane is down. Interactive tools leave it false to fail
		// fast with a clear connection error.
		nats.RetryOnFailedConnect(cfg.RetryConnect),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			c.mu.Lock()
			c.healthy = false
			c.mu.Unlock()
			c.logger.Warn("disconnected from NATS", "error", err)
			if cfg.OnDisconnect != nil {
				cfg.OnDisconnect(err)
			}
			select {
			case c.disconnectCh <- struct{}{}:
			default:
			}
		}),
		nats.ReconnectHandler(func(_ *nats.Conn) {
			c.mu.Lock()
			c.healthy = true
			c.mu.Unlock()
			c.logger.Info("reconnected to NATS")
			if cfg.OnReconnect != nil {
				cfg.OnReconnect()
			}
			select {
			case c.reconnectCh <- struct{}{}:
			default:
			}
		}),
		// ConnectHandler fires when the INITIAL connection is established.
		// This matters for RetryOnFailedConnect: nats.go completes a
		// deferred first connect inside its reconnect loop while initc is
		// still true, so it invokes ConnectedCB — NOT ReconnectedCB (which
		// is gated on !initc). Without this handler, a daemon that booted
		// while NATS was down would never flip healthy=true when the
		// control plane came back, and everything gated on IsHealthy()
		// (facts, settings, heartbeat, /readyz) would stay dormant forever.
		nats.ConnectHandler(func(nc *nats.Conn) {
			c.mu.Lock()
			already := c.healthy
			c.healthy = true
			c.mu.Unlock()
			if already {
				// Direct (non-deferred) connect: NewClient already
				// recorded and logged it; the initial connection is not
				// a "reconnect" for the metric hooks.
				return
			}
			c.logger.Info("connected to NATS",
				"url", nc.ConnectedUrl(),
				"server", nc.ConnectedServerName(),
			)
			if cfg.OnReconnect != nil {
				cfg.OnReconnect()
			}
			select {
			case c.reconnectCh <- struct{}{}:
			default:
			}
		}),
		nats.ClosedHandler(func(_ *nats.Conn) {
			c.mu.Lock()
			c.healthy = false
			c.mu.Unlock()
			c.logger.Info("NATS connection closed")
		}),
		nats.ErrorHandler(func(_ *nats.Conn, sub *nats.Subscription, err error) {
			subj := ""
			if sub != nil {
				subj = sub.Subject
			}
			c.logger.Error("NATS async error", "subject", subj, "error", err)
			if cfg.OnSlowConsumer != nil && errors.Is(err, nats.ErrSlowConsumer) {
				cfg.OnSlowConsumer()
			}
		}),
	}

	if cfg.TLS != nil {
		opts = append(opts, nats.Secure(cfg.TLS))
	}
	if cfg.CAFile != "" {
		// Deliberately NOT nats.RootCAs(): that helper reads the file
		// eagerly at option-application time and fails nats.Connect outright
		// when it is missing. Setting RootCAsCB directly defers every read
		// to the individual (re)connect attempt: rotation is picked up
		// without a restart, and a not-yet-materialized CA (provisioned
		// out-of-band) fails only that attempt — the retry/reconnect loop
		// heals once the file appears.
		caFile := cfg.CAFile
		optional := cfg.CAFileOptional
		if _, err := os.Stat(caFile); err != nil {
			if optional {
				cfg.Logger.Debug("conventional NATS CA file absent; using system trust store until it appears",
					"path", caFile)
			} else {
				cfg.Logger.Warn("NATS CA file not readable yet; connect attempts will retry until it appears",
					"path", caFile, "error", err)
			}
		}
		opts = append(opts, func(o *nats.Options) error {
			if o.TLSConfig == nil {
				o.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS13}
			}
			o.RootCAsCB = func() (*x509.CertPool, error) {
				pem, err := os.ReadFile(caFile)
				if err != nil {
					if optional && os.IsNotExist(err) {
						// nil pool = system trust store for this attempt;
						// the file is re-checked on the next one.
						return nil, nil
					}
					return nil, fmt.Errorf("bus: read NATS CA: %w", err)
				}
				pool := x509.NewCertPool()
				if !pool.AppendCertsFromPEM(pem) {
					return nil, fmt.Errorf("bus: failed to parse NATS CA certificate from %q", caFile)
				}
				return pool, nil
			}
			o.Secure = true
			return nil
		})
	}
	if cfg.CredsFile != "" {
		opts = append(opts, nats.UserCredentials(cfg.CredsFile))
	}
	if cfg.NKeySeedFile != "" {
		opt, err := nats.NkeyOptionFromSeed(cfg.NKeySeedFile)
		if err != nil {
			return nil, fmt.Errorf("bus: load nkey seed: %w", err)
		}
		opts = append(opts, opt)
	}

	url := nats.DefaultURL
	if len(cfg.URLs) > 0 {
		url = strings.Join(cfg.URLs, ",")
	}

	nc, err := nats.Connect(url, opts...)
	if err != nil {
		return nil, fmt.Errorf("bus: connect to NATS: %w", err)
	}

	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("bus: create jetstream context: %w", err)
	}

	c.nc = nc
	c.js = js

	// Only ever promote to healthy here, never demote: the async
	// ConnectHandler may already have observed a deferred connect
	// completing between nats.Connect returning and this line, and
	// writing false back would undo it. Both writers hold c.mu.
	if nc.IsConnected() {
		c.mu.Lock()
		c.healthy = true
		c.mu.Unlock()
		c.logger.Info("connected to NATS",
			"url", nc.ConnectedUrl(),
			"server", nc.ConnectedServerName(),
		)
	} else {
		// RetryOnFailedConnect: the initial attempt failed and the client
		// is reconnecting in the background.
		c.logger.Warn("NATS not reachable yet, retrying in background",
			"urls", url,
		)
	}

	return c, nil
}

// Conn returns the underlying NATS connection.
func (c *Client) Conn() *nats.Conn {
	return c.nc
}

// SetServers replaces the client's server pool at runtime (nats.go v1.52.0
// SetServerPool) and forces a reconnect when the currently-connected server
// is no longer in the new set. Subscriptions are auto-resent and JetStream
// contexts survive — no client rebuild. Used by the peel's discovery-refresh
// path so a NATS migration is picked up without a process restart. The new
// URLs must be tls:// (the pool inherits the connection-level TLS + creds).
func (c *Client) SetServers(urls []string) error {
	if len(urls) == 0 {
		return fmt.Errorf("bus: SetServers: empty URL list")
	}
	if err := c.nc.SetServerPool(urls); err != nil {
		return fmt.Errorf("bus: set server pool: %w", err)
	}
	// Only force a reconnect when the currently-connected server is genuinely
	// not in the new pool. nats.go normalizes pool URLs (e.g. it appends the
	// default :4222 to a port-less host), so ConnectedUrl() may differ
	// textually from an advertised entry that is the SAME endpoint — compare
	// on normalized (scheme, host, port) form, not raw strings, or an
	// unchanged port-less pool would force a needless reconnect on every apply.
	current := normalizeNATSEndpoint(c.nc.ConnectedUrl())
	stillListed := false
	for _, u := range urls {
		if normalizeNATSEndpoint(u) == current {
			stillListed = true
			break
		}
	}
	if !stillListed {
		if err := c.nc.ForceReconnect(); err != nil {
			return fmt.Errorf("bus: force reconnect: %w", err)
		}
	}
	return nil
}

// defaultNATSPort mirrors nats.go's default port, appended to a port-less URL
// when it normalizes the server pool.
const defaultNATSPort = "4222"

// normalizeNATSEndpoint canonicalizes a NATS URL for equality comparison:
// lowercased scheme + host and an explicit port (nats.go adds the default port
// to port-less URLs, so "tls://h" and "tls://h:4222" are the same endpoint).
// Unparseable input is returned trimmed and lowercased as a best-effort key.
func normalizeNATSEndpoint(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return strings.ToLower(raw)
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" {
		port = defaultNATSPort
	}
	return strings.ToLower(u.Scheme) + "://" + net.JoinHostPort(host, port)
}

// JetStream returns the bus adapter over the JetStream context. The adapter
// satisfies JetStreamAPI, ConsumerAPI, and ObjectStoreAPI; call Unwrap() for
// the raw jetstream.JetStream.
func (c *Client) JetStream() *JS {
	return NewJS(c.js)
}

// IsHealthy reports whether the client is currently connected and healthy.
// It is defensive: the cached flag OR the connection's own status counts,
// so the client never reports unhealthy while the connection is
// demonstrably up (belt-and-suspenders for connect transitions that fire
// no handler). A stale-true flag right after a drop is corrected within
// the same instant by nats.go's DisconnectErrHandler flipping it false.
func (c *Client) IsHealthy() bool {
	c.mu.RLock()
	healthy := c.healthy
	c.mu.RUnlock()
	return healthy || c.nc.IsConnected()
}

// DisconnectNotify returns a channel that receives when the client disconnects.
// The channel is buffered with size 1; non-blocking sends ensure no goroutine leak.
func (c *Client) DisconnectNotify() <-chan struct{} {
	return c.disconnectCh
}

// ReconnectNotify returns a channel that receives when the client reconnects.
func (c *Client) ReconnectNotify() <-chan struct{} {
	return c.reconnectCh
}

// Publish serializes v with MessagePack and publishes to the given subject.
func (c *Client) Publish(subject string, v any) error {
	data, err := Encode(v)
	if err != nil {
		return err
	}
	return c.nc.Publish(subject, data)
}

// PublishRaw publishes raw bytes to the given subject.
func (c *Client) PublishRaw(subject string, data []byte) error {
	return c.nc.Publish(subject, data)
}

// Request sends a MessagePack-encoded request and decodes the reply.
func (c *Client) Request(ctx context.Context, subject string, req any, resp any) error {
	data, err := Encode(req)
	if err != nil {
		return err
	}

	deadline, ok := ctx.Deadline()
	timeout := 5 * time.Second
	if ok {
		timeout = time.Until(deadline)
	}

	msg, err := c.nc.Request(subject, data, timeout)
	if err != nil {
		return fmt.Errorf("bus: request to %s: %w", subject, err)
	}

	if resp != nil {
		return Decode(msg.Data, resp)
	}
	return nil
}

// Subscribe creates a subscription that receives raw NATS messages.
func (c *Client) Subscribe(subject string, handler func(subject string, data []byte)) (*nats.Subscription, error) {
	return c.nc.Subscribe(subject, func(msg *nats.Msg) {
		handler(msg.Subject, msg.Data)
	})
}

// QueueSubscribe creates a queue subscription for load-balanced consumption.
func (c *Client) QueueSubscribe(subject, queue string, handler func(subject string, data []byte)) (*nats.Subscription, error) {
	return c.nc.QueueSubscribe(subject, queue, func(msg *nats.Msg) {
		handler(msg.Subject, msg.Data)
	})
}

// Flush flushes the connection buffer to the server.
func (c *Client) Flush() error {
	return c.nc.Flush()
}

// Shutdown gracefully drains and closes the client connection.
func (c *Client) Shutdown(ctx context.Context) error {
	c.logger.Info("shutting down NATS client")

	done := make(chan error, 1)
	go func() {
		done <- c.nc.Drain()
	}()

	select {
	case err := <-done:
		if err != nil {
			c.logger.Warn("drain error during shutdown", "error", err)
			c.nc.Close()
		}
	case <-ctx.Done():
		c.logger.Warn("shutdown context expired, forcing close")
		c.nc.Close()
	}

	c.logger.Info("NATS client stopped")
	return nil
}
