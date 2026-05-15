package pb

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/stone-age-io/stone-cli/internal/ctx"
)

// Client is a thin PocketBase REST wrapper.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
	Debug   bool // when true, log requests and responses to stderr
}

// debugBodyLimit caps the size of the body slice we log so a 4 MB pull
// response doesn't paint the terminal.
const debugBodyLimit = 4096

func logBody(buf []byte) string {
	if len(buf) == 0 {
		return "(empty)"
	}
	if len(buf) <= debugBodyLimit {
		return string(buf)
	}
	return string(buf[:debugBodyLimit]) + fmt.Sprintf("\n... (%d bytes truncated)", len(buf)-debugBodyLimit)
}

// New returns a client configured from the given context.
func New(c ctx.Context) *Client {
	return &Client{
		BaseURL: strings.TrimRight(c.URL, "/"),
		Token:   c.Auth.Token,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

// Record is the loose shape of a PocketBase record.
type Record map[string]any

// ListResult mirrors PocketBase's paginated list response.
type ListResult struct {
	Page       int      `json:"page"`
	PerPage    int      `json:"perPage"`
	TotalItems int      `json:"totalItems"`
	TotalPages int      `json:"totalPages"`
	Items      []Record `json:"items"`
}

// ListOptions configures the List call.
type ListOptions struct {
	Filter  string
	Sort    string
	Fields  string
	Expand  string
	Page    int
	PerPage int
}

// AuthResponse is returned by the auth-with-password endpoint.
type AuthResponse struct {
	Token  string `json:"token"`
	Record Record `json:"record"`
}

// AuthWithPassword authenticates against the given auth collection.
func (c *Client) AuthWithPassword(collection, identity, password string) (*AuthResponse, error) {
	body, err := json.Marshal(map[string]string{"identity": identity, "password": password})
	if err != nil {
		return nil, err
	}
	resp, err := c.do(http.MethodPost, "/api/collections/"+url.PathEscape(collection)+"/auth-with-password", bytes.NewReader(body), false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := checkOK(resp); err != nil {
		return nil, err
	}
	var ar AuthResponse
	if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil {
		return nil, fmt.Errorf("decode auth response: %w", err)
	}
	return &ar, nil
}

// Health pings the server.
func (c *Client) Health() error {
	resp, err := c.do(http.MethodGet, "/api/health", nil, false)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return checkOK(resp)
}

// List returns a page of records.
func (c *Client) List(collection string, opt ListOptions) (*ListResult, error) {
	q := url.Values{}
	if opt.Filter != "" {
		q.Set("filter", opt.Filter)
	}
	if opt.Sort != "" {
		q.Set("sort", opt.Sort)
	}
	if opt.Fields != "" {
		q.Set("fields", opt.Fields)
	}
	if opt.Expand != "" {
		q.Set("expand", opt.Expand)
	}
	if opt.Page > 0 {
		q.Set("page", strconv.Itoa(opt.Page))
	}
	if opt.PerPage > 0 {
		q.Set("perPage", strconv.Itoa(opt.PerPage))
	}
	path := "/api/collections/" + url.PathEscape(collection) + "/records"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	resp, err := c.do(http.MethodGet, path, nil, true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := checkOK(resp); err != nil {
		return nil, err
	}
	var lr ListResult
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		return nil, fmt.Errorf("decode list: %w", err)
	}
	return &lr, nil
}

// ListAll pages through all records matching the options.
func (c *Client) ListAll(collection string, opt ListOptions) ([]Record, error) {
	if opt.PerPage == 0 {
		opt.PerPage = 500
	}
	var all []Record
	page := 1
	for {
		opt.Page = page
		lr, err := c.List(collection, opt)
		if err != nil {
			return nil, err
		}
		all = append(all, lr.Items...)
		if page >= lr.TotalPages || len(lr.Items) == 0 {
			break
		}
		page++
	}
	return all, nil
}

// Get fetches a single record by id.
func (c *Client) Get(collection, id string) (Record, error) {
	resp, err := c.do(http.MethodGet, "/api/collections/"+url.PathEscape(collection)+"/records/"+url.PathEscape(id), nil, true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := checkOK(resp); err != nil {
		return nil, err
	}
	var r Record
	return r, json.NewDecoder(resp.Body).Decode(&r)
}

// Create posts a new record.
func (c *Client) Create(collection string, data Record) (Record, error) {
	body, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(http.MethodPost, "/api/collections/"+url.PathEscape(collection)+"/records", bytes.NewReader(body), true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := checkOK(resp); err != nil {
		return nil, err
	}
	var r Record
	return r, json.NewDecoder(resp.Body).Decode(&r)
}

// Update PATCHes a record by id.
func (c *Client) Update(collection, id string, data Record) (Record, error) {
	body, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(http.MethodPatch, "/api/collections/"+url.PathEscape(collection)+"/records/"+url.PathEscape(id), bytes.NewReader(body), true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := checkOK(resp); err != nil {
		return nil, err
	}
	var r Record
	return r, json.NewDecoder(resp.Body).Decode(&r)
}

// Delete removes a record by id.
func (c *Client) Delete(collection, id string) error {
	resp, err := c.do(http.MethodDelete, "/api/collections/"+url.PathEscape(collection)+"/records/"+url.PathEscape(id), nil, true)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return checkOK(resp)
}

// BatchOp is one operation in a /api/batch request.
type BatchOp struct {
	Method string         `json:"method"` // "POST" | "PATCH" | "DELETE"
	URL    string         `json:"url"`    // e.g. "/api/collections/things/records" or ".../records/<id>"
	Body   map[string]any `json:"body,omitempty"`
}

// BatchResponseItem is one result from a /api/batch request.
type BatchResponseItem struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
}

// Batch executes multiple operations transactionally. PocketBase returns
// per-op statuses; we surface a single error if the request itself failed.
func (c *Client) Batch(ops []BatchOp) ([]BatchResponseItem, error) {
	if len(ops) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(map[string]any{"requests": ops})
	if err != nil {
		return nil, err
	}
	resp, err := c.do(http.MethodPost, "/api/batch", bytes.NewReader(body), true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := checkOK(resp); err != nil {
		return nil, err
	}
	var items []BatchResponseItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, fmt.Errorf("decode batch: %w", err)
	}
	return items, nil
}

// do issues an HTTP request, optionally requiring auth.
func (c *Client) do(method, path string, body io.Reader, needAuth bool) (*http.Response, error) {
	if c.BaseURL == "" {
		return nil, errors.New("no PocketBase URL configured")
	}
	if needAuth && c.Token == "" {
		return nil, errors.New("not authenticated. run: stone auth login")
	}

	// When debug is on, buffer the request body so we can log it AND still
	// hand a fresh reader to http.NewRequest.
	var reqBytes []byte
	if c.Debug && body != nil {
		var err error
		reqBytes, err = io.ReadAll(body)
		if err != nil {
			return nil, fmt.Errorf("read request body for debug: %w", err)
		}
		body = bytes.NewReader(reqBytes)
	}

	req, err := http.NewRequest(method, c.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", c.Token)
	}

	if c.Debug {
		fmt.Fprintf(os.Stderr, "[debug] -> %s %s\n", method, req.URL.String())
		if len(reqBytes) > 0 {
			fmt.Fprintf(os.Stderr, "[debug]    body: %s\n", logBody(reqBytes))
		}
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		if c.Debug {
			fmt.Fprintf(os.Stderr, "[debug] <- transport error: %v\n", err)
		}
		return nil, err
	}

	if c.Debug {
		// Drain the body so we can log it, then replace it for the caller.
		respBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(respBytes))
		fmt.Fprintf(os.Stderr, "[debug] <- %d %s  body: %s\n", resp.StatusCode, http.StatusText(resp.StatusCode), logBody(respBytes))
	}
	return resp, nil
}

// PBError is the standard error shape PocketBase returns.
type PBError struct {
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data"`
}

func (e *PBError) Error() string {
	if len(e.Data) > 0 {
		return fmt.Sprintf("%s (%d): %s", e.Message, e.Code, formatPBData(e.Data))
	}
	return fmt.Sprintf("%s (%d)", e.Message, e.Code)
}

func formatPBData(d map[string]any) string {
	var parts []string
	for k, v := range d {
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	return strings.Join(parts, ", ")
}

func checkOK(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	b, _ := io.ReadAll(resp.Body)
	var pe PBError
	if err := json.Unmarshal(b, &pe); err == nil && pe.Message != "" {
		return &pe
	}
	if len(b) == 0 {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	return fmt.Errorf("http %d: %s", resp.StatusCode, string(b))
}

// DecodeJWTUserID extracts the user id from a PocketBase JWT without verifying.
// PocketBase puts the id under the "id" claim.
func DecodeJWTUserID(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", errors.New("invalid jwt format")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		// Try standard base64 in case padding is present.
		payload, err = base64.StdEncoding.DecodeString(parts[1])
		if err != nil {
			return "", fmt.Errorf("decode jwt payload: %w", err)
		}
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("parse jwt claims: %w", err)
	}
	id, _ := claims["id"].(string)
	if id == "" {
		return "", errors.New("jwt has no id claim")
	}
	return id, nil
}
