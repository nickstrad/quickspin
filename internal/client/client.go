// Package client speaks the control plane's JSON API. It is the same surface a
// published SDK would expose, which is why it decodes the shared httpapi
// envelope rather than defining wire types of its own.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strings"

	"github.com/nickstrad/quickspin/internal/httpapi"
	"github.com/nickstrad/quickspin/internal/runtime"
	"github.com/nickstrad/quickspin/internal/store"
)

// DefaultBaseURL is where `quickspin serve` listens by default.
const DefaultBaseURL = "http://127.0.0.1:8080"

type Client struct {
	baseURL string
	http    *http.Client
}

// New trims a trailing slash so callers may pass either form of the base URL.
// A nil httpClient takes http.DefaultClient, which has no timeout on purpose:
// exec can legitimately run for minutes, and the per-request deadline belongs to
// the caller's context.
func New(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		http:    httpClient,
	}
}

// Error is a response the server rejected. Code is the stable half of the
// envelope — branch on it, not on Message, which is free to be reworded.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("quickspin api: %s (HTTP %d)", e.Code, e.Status)
	}
	return fmt.Sprintf("quickspin api: %s (%s)", e.Message, e.Code)
}

// HasCode spares callers a type assertion. Sentinels do not survive the wire —
// the envelope's code is what a client branches on, which is the whole reason
// the code is the stable half.
func HasCode(err error, code string) bool {
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.Code == code
}

func (c *Client) CreateSandbox(ctx context.Context, idempotencyKey string, spec store.SpecFile) (*store.Sandbox, error) {
	var resp httpapi.SandboxResponse
	err := c.do(ctx, request{
		method:         http.MethodPost,
		path:           "/v1/sandboxes",
		body:           spec,
		idempotencyKey: idempotencyKey,
		out:            &resp,
	})
	if err != nil {
		return nil, err
	}
	return resp.Sandbox(), nil
}

func (c *Client) ListSandboxes(ctx context.Context) ([]*store.Sandbox, error) {
	var resp []httpapi.SandboxResponse
	if err := c.do(ctx, request{method: http.MethodGet, path: "/v1/sandboxes", out: &resp}); err != nil {
		return nil, err
	}
	sbxs := make([]*store.Sandbox, len(resp))
	for i, r := range resp {
		sbxs[i] = r.Sandbox()
	}
	return sbxs, nil
}

func (c *Client) InspectSandbox(ctx context.Context, sandboxID string) (runtime.Info, error) {
	var resp httpapi.InfoResponse
	err := c.do(ctx, request{method: http.MethodGet, path: sandboxPath(sandboxID), out: &resp})
	return resp.Info(), err
}

func (c *Client) DestroySandbox(ctx context.Context, sandboxID string) error {
	return c.do(ctx, request{method: http.MethodDelete, path: sandboxPath(sandboxID)})
}

func (c *Client) Exec(
	ctx context.Context,
	sandboxID string,
	cmd []string,
	opts runtime.ExecOpts,
) (runtime.ExecResult, error) {
	var resp httpapi.ExecResponse
	err := c.do(ctx, request{
		method: http.MethodPost,
		path:   sandboxPath(sandboxID) + "/exec",
		body:   httpapi.ExecRequest{Command: cmd, Options: httpapi.NewExecOptions(opts)},
		out:    &resp,
	})
	if err != nil {
		return runtime.ExecResult{}, err
	}
	return resp.ExecResult(), nil
}

func (c *Client) WriteFile(
	ctx context.Context,
	sandboxID, path string,
	content []byte,
	mode fs.FileMode,
) error {
	return c.do(ctx, request{
		method: http.MethodPut,
		path:   sandboxPath(sandboxID) + "/files",
		body: httpapi.WriteInSandboxRequest{
			Path:     path,
			Content:  content,
			FileMode: int64(mode),
		},
	})
}

func (c *Client) ReadFile(ctx context.Context, sandboxID, path string) ([]byte, error) {
	// The endpoint answers with a bare base64 JSON string, which is what a
	// []byte decodes from — no envelope to unwrap.
	var content []byte
	err := c.do(ctx, request{
		method: http.MethodGet,
		path:   sandboxPath(sandboxID) + "/files",
		query:  url.Values{"path": {path}},
		out:    &content,
	})
	return content, err
}

func (c *Client) ListDir(ctx context.Context, sandboxID, path string) ([]runtime.FileInfo, error) {
	var resp []httpapi.FileInfoResponse
	err := c.do(ctx, request{
		method: http.MethodGet,
		path:   sandboxPath(sandboxID) + "/dir",
		query:  url.Values{"path": {path}},
		out:    &resp,
	})
	if err != nil {
		return nil, err
	}
	infos := make([]runtime.FileInfo, len(resp))
	for i, r := range resp {
		infos[i] = r.FileInfo()
	}
	return infos, nil
}

func (c *Client) RemovePath(ctx context.Context, sandboxID, path string) error {
	return c.do(ctx, request{
		method: http.MethodDelete,
		path:   sandboxPath(sandboxID) + "/files",
		query:  url.Values{"path": {path}},
	})
}

// sandboxPath escapes the id so a caller-supplied string cannot inject a path
// segment; the server rejects malformed ids either way, but the URL should not
// be the place that decides.
func sandboxPath(sandboxID string) string {
	return "/v1/sandboxes/" + url.PathEscape(sandboxID)
}

type request struct {
	method string
	path   string
	query  url.Values
	// body is encoded as JSON when non-nil; out is decoded into when the
	// response carries one.
	body           any
	out            any
	idempotencyKey string
}

func (c *Client) do(ctx context.Context, req request) error {
	target := c.baseURL + req.path
	if len(req.query) > 0 {
		target += "?" + req.query.Encode()
	}

	var body io.Reader
	if req.body != nil {
		encoded, err := json.Marshal(req.body)
		if err != nil {
			return fmt.Errorf("encoding request for %s: %w", req.path, err)
		}
		body = bytes.NewReader(encoded)
	}

	httpReq, err := http.NewRequestWithContext(ctx, req.method, target, body)
	if err != nil {
		return fmt.Errorf("building request for %s: %w", req.path, err)
	}
	if req.body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	if req.idempotencyKey != "" {
		httpReq.Header.Set(httpapi.IdempotencyKeyHeader, req.idempotencyKey)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return fmt.Errorf("calling %s %s: %w", req.method, target, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		return decodeError(resp)
	}

	if req.out == nil || resp.StatusCode == http.StatusNoContent {
		// Drained rather than abandoned so the connection can be reused.
		io.Copy(io.Discard, resp.Body)
		return nil
	}

	if err := json.NewDecoder(resp.Body).Decode(req.out); err != nil {
		return fmt.Errorf("decoding response from %s: %w", req.path, err)
	}
	return nil
}

// decodeError falls back to the status alone when the body is not the envelope:
// a proxy or a panic upstream can answer with HTML, and that must still surface
// as a typed failure rather than a decode error about the error.
func decodeError(resp *http.Response) error {
	apiErr := &Error{
		Status: resp.StatusCode,
		Code:   httpapi.CodeForStatus(resp.StatusCode),
	}

	var envelope httpapi.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err == nil && envelope.Error.Code != "" {
		apiErr.Code = envelope.Error.Code
		apiErr.Message = envelope.Error.Message
	}
	return apiErr
}
