package linear

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// blackHoleProxy forwards TCP connections to target until freeze is called; from then
// on the connections it already has stop carrying bytes either way without closing,
// like a connection the network dropped silently. New connections still work.
type blackHoleProxy struct {
	ln     net.Listener
	target string

	mu     sync.Mutex
	frozen []chan struct{}
}

func newBlackHoleProxy(t *testing.T, target string) *blackHoleProxy {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	p := &blackHoleProxy{ln: ln, target: target}
	t.Cleanup(func() { _ = ln.Close() })
	go p.serve()
	return p
}

func (p *blackHoleProxy) serve() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		up, err := net.Dial("tcp", p.target)
		if err != nil {
			_ = c.Close()
			continue
		}
		stop := make(chan struct{})
		p.mu.Lock()
		p.frozen = append(p.frozen, stop)
		p.mu.Unlock()
		go pipe(c, up, stop)
		go pipe(up, c, stop)
	}
}

// pipe copies src to dst until stop is closed, then reads and drops everything.
func pipe(dst, src net.Conn, stop chan struct{}) {
	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if err != nil {
			_ = dst.Close()
			return
		}
		select {
		case <-stop:
			continue
		default:
		}
		if _, err := dst.Write(buf[:n]); err != nil {
			return
		}
	}
}

func (p *blackHoleProxy) freeze() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, stop := range p.frozen {
		close(stop)
	}
	p.frozen = nil
}

// A silently dropped HTTP/2 connection used to take every request down with it until
// the kernel gave up on it (about 15 minutes). The health check closes it instead and
// the next request dials a new connection.
func TestDeadConnectionIsReplaced(t *testing.T) {
	defer func(after, timeout time.Duration) { http2PingAfter, http2PingTimeout = after, timeout }(http2PingAfter, http2PingTimeout)
	http2PingAfter, http2PingTimeout = 200*time.Millisecond, 200*time.Millisecond

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte(`{"data":{"viewer":{"name":"me"}}}`))
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	proxy := newBlackHoleProxy(t, srv.Listener.Addr().String())

	c := NewClient("key")
	c.endpoint = "https://" + proxy.ln.Addr().String()
	tr := c.http.Transport.(*http.Transport)
	tr.TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	c.http.Timeout = 5 * time.Second

	var out struct {
		Viewer struct{ Name string } `json:"viewer"`
	}
	query := func() error { return c.do(context.Background(), "{viewer{name}}", nil, &out) }

	require.NoError(t, query())
	proxy.freeze()

	start := time.Now()
	assert.Error(t, query(), "the request in flight on the dead connection fails")
	assert.Less(t, time.Since(start), 3*time.Second, "closed by the health check, not the client timeout")

	require.NoError(t, query(), "the next request gets a new connection")
	assert.Equal(t, "me", out.Viewer.Name)
}
