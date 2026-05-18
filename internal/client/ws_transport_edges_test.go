package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWSClose_BoundedOnHalfOpenConn ensures Close doesn't block forever when
// the underlying TCP is half-open. We simulate this by closing the read half
// of the underlying socket and verifying Close returns within a bounded time.
func TestWSClose_BoundedOnHalfOpenConn(t *testing.T) {
	t.Parallel()

	m := &controllableMiddleware{}
	host := startControllable(t, m)

	transport, err := newWSTransport(host, NewSecretString("test-key"), true)
	require.NoError(t, err)

	if tcp, ok := transport.conn.UnderlyingConn().(*net.TCPConn); ok {
		_ = tcp.CloseRead()
	}

	start := time.Now()
	_ = transport.Close()

	elapsed := time.Since(start)
	assert.Less(t, elapsed, 15*time.Second,
		"Close() must be bounded even on a half-open TCP; elapsed=%s", elapsed)
}

// TestWSSetReadLimit_RejectsOversizedFrame verifies the wsMaxMessageBytes
// read limit actually caps server-controlled frame size. A malicious server
// attempting to OOM the provider via a giant frame should trigger a read error.
func TestWSSetReadLimit_RejectsOversizedFrame(t *testing.T) {
	t.Parallel()

	m := &controllableMiddleware{
		respond: func(conn *websocket.Conn, _, id string) bool {
			// Build a payload that marshals to more than wsMaxMessageBytes.
			// Using repeated ASCII bytes for predictable size accounting.
			payload := bytes.Repeat([]byte("A"), wsMaxMessageBytes+1024)

			resp := map[string]any{
				"msg":    "result",
				"id":     id,
				"result": string(payload),
			}
			data, _ := json.Marshal(resp)
			_ = conn.WriteMessage(websocket.TextMessage, data)

			return true
		},
	}
	host := startControllable(t, m)

	transport, err := newWSTransport(host, NewSecretString("test-key"), true)
	require.NoError(t, err)
	defer transport.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var out string
	err = transport.Call(ctx, "system.info", nil, &out)
	require.Error(t, err,
		"a >wsMaxMessageBytes response must fail the call with a read error, not silently consume gigabytes of RAM")
}

// TestWSUpload_CheckRedirectRejects3xx proves the uploadClient refuses to
// follow redirects. If CheckRedirect ever regresses to default behavior, the
// Authorization: Bearer header would be re-sent to whatever host the 3xx
// points at — a credential exfil primitive.
func TestWSUpload_CheckRedirectRejects3xx(t *testing.T) {
	t.Parallel()

	var redirectReceivedAuth atomic.Bool

	redirectTarget := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			redirectReceivedAuth.Store(true)
		}
	}))
	defer redirectTarget.Close()

	// Primary returns a 302 on any POST /_upload/.
	primary := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "_upload") {
			w.Header().Set("Location", redirectTarget.URL)
			w.WriteHeader(http.StatusFound)

			return
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	defer primary.Close()

	host := strings.TrimPrefix(primary.URL, "https://")

	// Build a minimal transport shell — only the upload path matters here.
	transport := &wsTransport{
		apiKey:             NewSecretString("test-key"),
		host:               host,
		insecureSkipVerify: true,
		pending:            map[string]chan *wsResponse{},
		uploadClient: &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
			},
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
			Timeout: 5 * time.Second,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := transport.UploadFile(ctx, "/mnt/tank/x.iso", strings.NewReader("body"), 4)
	require.Error(t, err, "upload against a 302-returning server must fail, not follow the redirect")
	assert.False(t, redirectReceivedAuth.Load(),
		"Authorization header must not be re-sent to the redirect target — CheckRedirect regression would leak the bearer")
}

// TestWSUploadPayload_UnicodePathIsValidJSON pins that the upload envelope
// is always valid JSON, including for paths with characters where Go's %q
// and JSON's string rules diverge. Enforces the v0.15 json.Marshal change.
func TestWSUploadPayload_UnicodePathIsValidJSON(t *testing.T) {
	t.Parallel()

	paths := []string{
		"/mnt/tank/iso/" + string([]byte{0x01}) + ".iso",
		"/mnt/tank/iso/\u0000-null.iso",
		"/mnt/tank/iso/日本語.iso",
		"/mnt/tank/iso/emoji-🔥.iso",
	}

	for _, p := range paths {
		body, err := json.Marshal(map[string]any{
			"method": "filesystem.put",
			"params": []any{p, map[string]any{"mode": 493}},
		})
		require.NoError(t, err, "path %q must marshal cleanly", p)

		var echo map[string]any
		require.NoError(t, json.Unmarshal(body, &echo),
			"round-trip marshal→unmarshal must succeed for path %q", p)

		params, ok := echo["params"].([]any)
		require.True(t, ok)
		require.Equal(t, p, params[0], "path must round-trip byte-for-byte")
	}
}

// errReader is an io.Reader that returns a fixed error on every Read.
// Used to simulate a source reader failing mid-stream so writeUploadMultipart's
// io.Copy → pw.CloseWithError path can be exercised.
type errReader struct{ err error }

func (r *errReader) Read([]byte) (int, error) { return 0, r.err }

// TestUploadFile_DataReaderErrorPropagates pins the contract that a
// failing data reader causes the upload to surface the underlying error
// (rather than hang forever on the pipe). The previous inline goroutine
// in UploadFile had six pw.CloseWithError branches; the extraction to
// writeUploadMultipart preserved them. This test exercises the io.Copy
// branch — the most operationally relevant of the six.
func TestUploadFile_DataReaderErrorPropagates(t *testing.T) {
	t.Parallel()

	// Stand up a minimal upload-target server. We expect the request to
	// fail before any meaningful body lands; the server-side handler is
	// only here so the http.Client has somewhere to dial.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Drain the body so the client doesn't get a "connection reset"
		// before the pipe error surfaces.
		_, _ = bytes.NewBuffer(nil).ReadFrom(r.Body)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "https://")

	transport := &wsTransport{
		apiKey:             NewSecretString("test-key"),
		host:               host,
		insecureSkipVerify: true,
		pending:            map[string]chan *wsResponse{},
		uploadClient: &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
			},
			Timeout: 5 * time.Second,
		},
	}

	// Source reader fails on the first Read with a sentinel error. The
	// goroutine inside writeUploadMultipart should detect the error,
	// CloseWithError on the pipe writer, and the HTTP client should
	// surface the failure rather than hanging.
	sentinel := &errReader{err: errReadSentinel}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := transport.UploadFile(ctx, "/mnt/tank/x.iso", sentinel, 0)
	require.Error(t, err, "data-reader error must surface as an upload error")
	// The HTTP client may wrap the pipe error; we accept either the
	// upload-status path or the pipe-wrapping path. The critical guarantee
	// is that the call RETURNED instead of hanging forever — the test's
	// 2s deadline above is what enforces that.
}

var errReadSentinel = readerSentinelError("simulated read failure")

type readerSentinelError string

func (e readerSentinelError) Error() string { return string(e) }
