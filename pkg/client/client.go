// Package client provides the context-aware Arcana v0.1 HTTP and WebSocket client.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/brendatamaar/arcana/internal/api/gen"
)

// Sandbox and related API models share the generated v0.1 wire contract.
type (
	// Sandbox uses the generated v0.1 wire model.
	Sandbox = gen.Sandbox
	// SandboxList uses the generated v0.1 wire model.
	SandboxList = gen.SandboxList
	// SandboxState uses the generated v0.1 wire model.
	SandboxState = gen.SandboxState
	// CreateSandboxRequest uses the generated v0.1 wire model.
	CreateSandboxRequest = gen.CreateSandbox
	// Resources uses the generated v0.1 wire model.
	Resources = gen.Resources
	// Network uses the generated v0.1 wire model.
	Network = gen.Network
	// Key uses the generated v0.1 wire model.
	Key = gen.Key
	// KeyList uses the generated v0.1 wire model.
	KeyList = gen.KeyList
	// CreateKeyRequest uses the generated v0.1 wire model.
	CreateKeyRequest = gen.CreateKey
	// CreatedKey uses the generated v0.1 wire model.
	CreatedKey = gen.CreatedKey
	// FileEntry uses the generated v0.1 wire model.
	FileEntry = gen.FileEntry
	// FileList uses the generated v0.1 wire model.
	FileList = gen.FileList
	// ExecResult uses the generated v0.1 wire model.
	ExecResult = gen.ExecResult
	// Health uses the generated v0.1 wire model.
	Health = gen.Health
)

// ExecRequest selects exactly one of Cmd or Shell. Stdin is encoded as base64.
type ExecRequest struct {
	Cmd            []string          `json:"cmd,omitempty"`
	Shell          *string           `json:"shell,omitempty"`
	Cwd            string            `json:"cwd,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	TimeoutSeconds *int64            `json:"timeout_seconds,omitempty"`
	Stdin          []byte            `json:"stdin,omitempty"`
}

// Options configures transport and finite response limits. Zero selects defaults.
// HTTPClient is copied; redirects are rejected to avoid forwarding credentials.
type Options struct {
	HTTPClient       *http.Client
	MaxResponseBytes int64
	MaxTransferBytes int64
}

// Client is safe for concurrent use. It never retries mutations automatically.
type Client struct {
	base                     *url.URL
	key                      string
	http                     *http.Client
	jsonLimit, transferLimit int64
}

// ErrLimit reports a response or transfer exceeding the configured byte cap.
var ErrLimit = errors.New("arcana client byte limit exceeded")

// Error is an API failure; errors.As exposes Code, StatusCode and RequestID.
type Error struct {
	StatusCode               int
	Code, Message, RequestID string
}

func (e *Error) Error() string {
	return fmt.Sprintf("arcana: HTTP %d (%s): %s", e.StatusCode, e.Code, e.Message)
}

// New constructs a client for a server origin or reverse-proxy base path.
func New(server, apiKey string, options Options) (*Client, error) {
	base, err := url.Parse(server)
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("invalid Arcana server URL")
	}
	if strings.ContainsAny(apiKey, "\r\n") {
		return nil, errors.New("invalid API key")
	}
	jsonLimit, transferLimit := options.MaxResponseBytes, options.MaxTransferBytes
	if jsonLimit == 0 {
		jsonLimit = 64 << 20
	}
	if transferLimit == 0 {
		transferLimit = 512 << 20
	}
	if jsonLimit < 1 || jsonLimit > 1<<30 || transferLimit < 1 || transferLimit > 1<<30 {
		return nil, errors.New("client limits must be between 1 byte and 1 GiB")
	}
	transport := http.Client{}
	if options.HTTPClient != nil {
		transport = *options.HTTPClient
	}
	transport.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{base, apiKey, &transport, jsonLimit, transferLimit}, nil
}

func (c *Client) endpoint(path string, query url.Values) string {
	u := *c.base
	u.Path = strings.TrimRight(u.Path, "/") + "/v1" + path
	u.RawPath = ""
	u.RawQuery = query.Encode()
	return u.String()
}

func resource(id string) (string, error) {
	if id == "" || len(id) > 128 || strings.ContainsAny(id, "/\\?#%") || id == "." || id == ".." {
		return "", errors.New("invalid resource ID")
	}
	return "/sandboxes/" + id, nil
}

func (c *Client) request(ctx context.Context, method, path string, query url.Values, body io.Reader, media string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint(path, query), body)
	if err != nil {
		return nil, err
	}
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	if media != "" {
		req.Header.Set("Content-Type", media)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("arcana request: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		return nil, decodeError(resp)
	}
	return resp, nil
}

func decodeError(resp *http.Response) error {
	var envelope struct {
		Error struct {
			Code, Message string
			RequestID     string `json:"request_id"`
		}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil || len(data) > 65536 || json.Unmarshal(data, &envelope) != nil || envelope.Error.Code == "" {
		return &Error{StatusCode: resp.StatusCode, Code: "invalid_response", Message: "server returned an invalid error envelope"}
	}
	return &Error{resp.StatusCode, envelope.Error.Code, envelope.Error.Message, envelope.Error.RequestID}
}

func (c *Client) json(ctx context.Context, method, path string, q url.Values, input, output any) error {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		if int64(len(data)) > c.jsonLimit {
			return ErrLimit
		}
		body = bytes.NewReader(data)
	}
	resp, err := c.request(ctx, method, path, q, body, "application/json")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, c.jsonLimit+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > c.jsonLimit {
		return ErrLimit
	}
	if output == nil {
		return nil
	}
	if err = json.Unmarshal(data, output); err != nil {
		return fmt.Errorf("decoding Arcana response: %w", err)
	}
	return nil
}

// ListOptions selects one bounded page; callers advance using NextCursor.
type ListOptions struct {
	Limit                int64
	Cursor, State, Label string
}

func (o ListOptions) query() url.Values {
	q := url.Values{}
	if o.Limit != 0 {
		q.Set("limit", strconv.FormatInt(o.Limit, 10))
	}
	if o.Cursor != "" {
		q.Set("cursor", o.Cursor)
	}
	if o.State != "" {
		q.Set("state", o.State)
	}
	if o.Label != "" {
		q.Set("label", o.Label)
	}
	return q
}

// Health checks server liveness.
func (c *Client) Health(ctx context.Context) (Health, error) {
	var out Health
	err := c.json(ctx, "GET", "/health", nil, nil, &out)
	return out, err
}

// CreateSandbox creates a sandbox with optional template overrides.
func (c *Client) CreateSandbox(ctx context.Context, in CreateSandboxRequest) (Sandbox, error) {
	var out Sandbox
	err := c.json(ctx, "POST", "/sandboxes", nil, in, &out)
	return out, err
}

// ListSandboxes retrieves one page visible to the current key.
func (c *Client) ListSandboxes(ctx context.Context, o ListOptions) (SandboxList, error) {
	var out SandboxList
	err := c.json(ctx, "GET", "/sandboxes", o.query(), nil, &out)
	return out, err
}

// GetSandbox retrieves one owned sandbox.
func (c *Client) GetSandbox(ctx context.Context, id string) (Sandbox, error) {
	var out Sandbox
	p, err := resource(id)
	if err == nil {
		err = c.json(ctx, "GET", p, nil, nil, &out)
	}
	return out, err
}

// DestroySandbox idempotently destroys a sandbox.
func (c *Client) DestroySandbox(ctx context.Context, id string) error {
	p, err := resource(id)
	if err != nil {
		return err
	}
	return c.json(ctx, "DELETE", p, nil, nil, nil)
}

// Keepalive resets idle activity without extending absolute TTL.
func (c *Client) Keepalive(ctx context.Context, id string) (Sandbox, error) {
	var out Sandbox
	p, err := resource(id)
	if err == nil {
		err = c.json(ctx, "POST", p+"/keepalive", nil, nil, &out)
	}
	return out, err
}

// Exec runs a synchronous command and returns its actual exit code.
func (c *Client) Exec(ctx context.Context, id string, in ExecRequest) (ExecResult, error) {
	var out ExecResult
	p, err := resource(id)
	if err == nil {
		err = c.json(ctx, "POST", p+"/exec", nil, in, &out)
	}
	return out, err
}

// CreateKey creates an API key; its secret is returned only once.
func (c *Client) CreateKey(ctx context.Context, in CreateKeyRequest) (CreatedKey, error) {
	var out CreatedKey
	err := c.json(ctx, "POST", "/keys", nil, in, &out)
	return out, err
}

// ListKeys retrieves one administrator-only key page.
func (c *Client) ListKeys(ctx context.Context, o ListOptions) (KeyList, error) {
	var out KeyList
	err := c.json(ctx, "GET", "/keys", o.query(), nil, &out)
	return out, err
}

// RevokeKey revokes a key by ID.
func (c *Client) RevokeKey(ctx context.Context, id string) error {
	if _, err := resource(id); err != nil {
		return err
	}
	return c.json(ctx, "DELETE", "/keys/"+id, nil, nil, nil)
}

type boundedBody struct {
	io.ReadCloser
	remaining int64
}

func (b *boundedBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if b.remaining == 0 {
		var probe [1]byte
		n, err := b.ReadCloser.Read(probe[:])
		if n > 0 {
			return 0, ErrLimit
		}
		return 0, err
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.ReadCloser.Read(p)
	b.remaining -= int64(n)
	return n, err
}

func (c *Client) download(ctx context.Context, id, path, suffix string) (io.ReadCloser, error) {
	p, err := resource(id)
	if err != nil {
		return nil, err
	}
	resp, err := c.request(ctx, "GET", p+suffix, url.Values{"path": {path}}, nil, "")
	if err != nil {
		return nil, err
	}
	if resp.ContentLength > c.transferLimit {
		_ = resp.Body.Close()
		return nil, ErrLimit
	}
	return &boundedBody{resp.Body, c.transferLimit}, nil
}

func (c *Client) upload(ctx context.Context, id, path, suffix, media string, input io.Reader) error {
	p, err := resource(id)
	if err != nil {
		return err
	}
	if input == nil {
		return errors.New("nil transfer reader")
	}
	body := &boundedBody{io.NopCloser(input), c.transferLimit}
	resp, err := c.request(ctx, "PUT", p+suffix, url.Values{"path": {path}}, body, media)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// ReadFile returns a bounded body; callers must close it, including on read failure.
func (c *Client) ReadFile(ctx context.Context, id, path string) (io.ReadCloser, error) {
	return c.download(ctx, id, path, "/files")
}

// WriteFile streams input into an atomic sandbox-local write.
func (c *Client) WriteFile(ctx context.Context, id, path string, input io.Reader) error {
	return c.upload(ctx, id, path, "/files", "application/octet-stream", input)
}

// DownloadArchive returns opaque gzip data. Callers must close the body.
func (c *Client) DownloadArchive(ctx context.Context, id, path string) (io.ReadCloser, error) {
	return c.download(ctx, id, path, "/files/archive")
}

// UploadArchive streams opaque gzip data for extraction inside the sandbox.
func (c *Client) UploadArchive(ctx context.Context, id, path string, input io.Reader) error {
	return c.upload(ctx, id, path, "/files/archive", "application/gzip", input)
}

// DeleteFile removes a sandbox path, optionally recursively.
func (c *Client) DeleteFile(ctx context.Context, id, path string, recursive bool) error {
	p, err := resource(id)
	if err != nil {
		return err
	}
	return c.json(ctx, "DELETE", p+"/files", url.Values{"path": {path}, "recursive": {strconv.FormatBool(recursive)}}, nil, nil)
}

// ListFiles retrieves a directory listing with a bounded recursion depth.
func (c *Client) ListFiles(ctx context.Context, id, path string, depth int64) (FileList, error) {
	var out FileList
	p, err := resource(id)
	if err == nil {
		err = c.json(ctx, "GET", p+"/files/list", url.Values{"path": {path}, "depth": {strconv.FormatInt(depth, 10)}}, nil, &out)
	}
	return out, err
}
