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

// GetOptions narrows a single-record fetch.
type GetOptions struct {
	Fields string // comma-separated server-side field projection
	Expand string // comma-separated relations to return inline under "expand"
}

// Get fetches a single record by id.
func (c *Client) Get(collection, id string, opts ...GetOptions) (Record, error) {
	path := "/api/collections/" + url.PathEscape(collection) + "/records/" + url.PathEscape(id)
	if len(opts) > 0 {
		q := url.Values{}
		if opts[0].Fields != "" {
			q.Set("fields", opts[0].Fields)
		}
		if opts[0].Expand != "" {
			q.Set("expand", opts[0].Expand)
		}
		if len(q) > 0 {
			path += "?" + q.Encode()
		}
	}
	resp, err := c.do(http.MethodGet, path, nil, true)
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
	body, err := json.Marshal(withAuthDefaults(data))
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
	body, err := json.Marshal(withAuthDefaults(data))
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

// CallRoute POSTs to one of the platform's custom (non-collection) routes and
// decodes the JSON response into out, which may be nil.
//
// These routes exist because a PocketBase API rule cannot express a
// single-field allowlist. Credential rotation and account key management each
// need to permit a write to exactly one field and forbid every other, which a
// rule can only approximate with `:isset = false` on everything else — a
// deny-list that silently opens up when a field is added. The platform's answer
// is a route that takes no record id (the target is derived from the caller's
// own identity or active organization) and a switch that maps each action to
// one field. See the platform's hooks/credential_routes.go and
// hooks/nats_account_routes.go.
//
// body may be nil for routes that take no payload.
func (c *Client) CallRoute(path string, body any, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	resp, err := c.do(http.MethodPost, path, r, true)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := checkOK(resp); err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// GetRoute is CallRoute for the platform's read-only routes.
//
// Separate rather than a method parameter on CallRoute: every caller of that one
// is performing an action, and its doc comment is about why those actions are
// routes at all. A read is there for a different reason -- the platform's
// certificate audit has to parse a Nebula certificate to answer, which no
// client can do -- and collapsing the two would put an unused body argument in
// front of every read.
func (c *Client) GetRoute(path string, out any) error {
	resp, err := c.do(http.MethodGet, path, nil, true)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := checkOK(resp); err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
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
	for i := range ops {
		ops[i].Body = withAuthDefaults(ops[i].Body)
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

// withAuthDefaults fills in fields PocketBase auth collections require but
// our typed CRUD layer doesn't model: passwordConfirm (must match password)
// and emailVisibility (defaults to true so things/nats_users/nebula_hosts
// are addressable by email). Triggered by the presence of a non-empty
// password, which reliably signals an auth-collection create or password
// change. Callers that explicitly set either field keep their value.
func withAuthDefaults(data Record) Record {
	pw, ok := data["password"].(string)
	if !ok || pw == "" {
		return data
	}
	_, hasConfirm := data["passwordConfirm"]
	_, hasVisibility := data["emailVisibility"]
	if hasConfirm && hasVisibility {
		return data
	}
	out := make(Record, len(data)+2)
	for k, v := range data {
		out[k] = v
	}
	if !hasConfirm {
		out["passwordConfirm"] = pw
	}
	if !hasVisibility {
		out["emailVisibility"] = true
	}
	return out
}

// do issues an HTTP request, optionally requiring auth.
func (c *Client) do(method, path string, body io.Reader, needAuth bool) (*http.Response, error) {
	if c.BaseURL == "" {
		return nil, errors.New("no PocketBase URL configured")
	}
	if needAuth && c.Token == "" {
		return nil, errors.New("not authenticated. run: stone auth login")
	}
	// An expired token is refused HERE rather than left to the server, because
	// the server does not refuse it either. PocketBase treats a token it will
	// not accept as no token at all: the request proceeds as a guest, every
	// list rule filters it down to nothing, and the response is 200 with an
	// empty items array. So an aged-out session looks exactly like an empty
	// organization -- `stone thing ls` prints a header and no rows, `stone org
	// ls` says "no organizations visible to this user", and nothing anywhere
	// says the word "login". That is a wrong answer delivered confidently,
	// which is worse than an error.
	//
	// Checked against the `exp` claim rather than a stored timestamp so it also
	// covers contexts written before the CLI recorded one. A token whose expiry
	// cannot be parsed is sent as-is (see DecodeJWTExpiry).
	if needAuth && TokenExpired(c.Token) {
		exp, _ := DecodeJWTExpiry(c.Token)
		return nil, fmt.Errorf("the session for this context expired %s — run: stone auth login",
			exp.Local().Format(time.RFC1123))
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
//
// The status arrives as `status`, not `code` -- PocketBase's ApiError marshals
// it under that name, and has for every version this CLI has talked to. Reading
// only `code` meant the number in every error message the CLI has ever printed
// was 0, including on the ones where it matters most: a 404 from an update rule
// and a 400 from a validator read identically. Both names are accepted now, and
// checkOK fills in the HTTP status if a server sends neither.
type PBError struct {
	Code    int            `json:"code"`
	Status  int            `json:"status"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data"`
}

// status is whichever the server actually sent.
func (e *PBError) status() int {
	if e.Status != 0 {
		return e.Status
	}
	return e.Code
}

// authHint is appended to a 401. PocketBase phrases that status as "The request
// requires valid record authorization token to be set", which is accurate and
// tells nobody what to do about it. It is the belt to the expired-token check's
// braces in `do`: that one catches the common case before a request goes out,
// this one covers whatever the server does reject outright. A 403 deliberately
// gets no hint -- that one means the token is fine and the role is not, and
// logging in again would not change it.
const authHint = "\n  the session token is missing or expired — run: stone auth login"

// collectionHint is appended when PocketBase reports an unknown collection.
// "Missing collection context." is its wording, and on its own it reads like a
// client bug. It nearly always means the opposite: the CLI knows about a
// collection this deployment does not have yet, because the server is older
// than the release that added it. That is the normal shape of a CLI that ships
// separately from the platform, and it deserves to say so rather than leave
// someone reading PocketBase's source.
const collectionHint = "\n  this server has no such collection — it is probably running a platform release older than this CLI"

func (e *PBError) Error() string {
	var msg string
	if len(e.Data) > 0 {
		msg = fmt.Sprintf("%s (%d): %s", e.Message, e.status(), formatPBData(e.Data))
	} else {
		msg = fmt.Sprintf("%s (%d)", e.Message, e.status())
	}
	switch {
	case e.status() == http.StatusUnauthorized:
		msg += authHint
	case e.status() == http.StatusNotFound && strings.Contains(e.Message, "Missing collection"):
		msg += collectionHint
	}
	return msg
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
		if pe.status() == 0 {
			pe.Status = resp.StatusCode
		}
		return &pe
	}
	if len(b) == 0 {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	return fmt.Errorf("http %d: %s", resp.StatusCode, string(b))
}

// decodeJWTClaims parses a JWT payload without verifying the signature. That is
// deliberate and safe for what this file does with it: the server verifies, and
// the two things read out here -- the caller's own id and the expiry -- are used
// to fill in local state and to fail early. Neither decides anything the server
// would not decide again.
func decodeJWTClaims(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("invalid jwt format")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		// Try standard base64 in case padding is present.
		payload, err = base64.StdEncoding.DecodeString(parts[1])
		if err != nil {
			return nil, fmt.Errorf("decode jwt payload: %w", err)
		}
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("parse jwt claims: %w", err)
	}
	return claims, nil
}

// DecodeJWTUserID extracts the user id from a PocketBase JWT without verifying.
// PocketBase puts the id under the "id" claim.
func DecodeJWTUserID(token string) (string, error) {
	claims, err := decodeJWTClaims(token)
	if err != nil {
		return "", err
	}
	id, _ := claims["id"].(string)
	if id == "" {
		return "", errors.New("jwt has no id claim")
	}
	return id, nil
}

// DecodeJWTExpiry returns the token's `exp` claim as a time. A token without one
// returns the zero time and no error: that is "unknown", not "expired", and the
// caller must treat it as such -- refusing to send a token this CLI could not
// parse would turn a format change into an outage.
func DecodeJWTExpiry(token string) (time.Time, error) {
	claims, err := decodeJWTClaims(token)
	if err != nil {
		return time.Time{}, err
	}
	// JSON numbers decode as float64. PocketBase writes exp as seconds since the
	// epoch, and float64 holds those exactly for any date anyone will see.
	exp, ok := claims["exp"].(float64)
	if !ok {
		return time.Time{}, nil
	}
	return time.Unix(int64(exp), 0), nil
}

// TokenExpired reports whether the token's expiry is in the past. A token whose
// expiry cannot be read is never reported as expired -- see DecodeJWTExpiry.
func TokenExpired(token string) bool {
	exp, err := DecodeJWTExpiry(token)
	if err != nil || exp.IsZero() {
		return false
	}
	return time.Now().After(exp)
}
