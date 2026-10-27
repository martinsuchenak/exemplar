// Package client is a typed HTTP client for the exemplar REST API. The CLI
// uses it for every command that does not run on the host, so remote and
// local instances are used the same way and the server's services enforce
// all access rules.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/martinsuchenak/exemplar/internal/account"
	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/audit"
	"github.com/martinsuchenak/exemplar/internal/dataset"
	"github.com/martinsuchenak/exemplar/internal/exchange"
	"github.com/martinsuchenak/exemplar/internal/fixture"
	"github.com/martinsuchenak/exemplar/internal/generate"
	"github.com/martinsuchenak/exemplar/internal/ingest"
	"github.com/martinsuchenak/exemplar/internal/person"
	"github.com/martinsuchenak/exemplar/internal/synth"
)

// Client calls one exemplar server with one API key.
type Client struct {
	base *url.URL
	key  string
	http *http.Client
	// stream has no overall timeout, for archives; the context limits it.
	stream *http.Client
}

// New returns a client for the server at baseURL (for example
// http://localhost:8080) using apiKey.
func New(baseURL, apiKey string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%w: server must be an http(s) URL, got %q", apperr.ErrInvalid, baseURL)
	}
	if apiKey == "" {
		return nil, fmt.Errorf("%w: an API key is required (--api-key or EXEMPLAR_API_KEY)", apperr.ErrInvalid)
	}
	return &Client{base: u, key: apiKey, http: &http.Client{Timeout: 10 * time.Minute}, stream: &http.Client{}}, nil
}

// Error is a non-2xx response. It unwraps to the matching apperr kind, so
// callers can use errors.Is(err, apperr.ErrNotFound) as with the services.
type Error struct {
	Status  int
	Message string
}

// Error returns the server's message.
func (e *Error) Error() string {
	return e.Message
}

// Unwrap returns the apperr kind for the status code, or nil.
func (e *Error) Unwrap() error {
	switch e.Status {
	case http.StatusBadRequest:
		return apperr.ErrInvalid
	case http.StatusUnauthorized:
		return apperr.ErrUnauthenticated
	case http.StatusForbidden:
		return apperr.ErrForbidden
	case http.StatusNotFound:
		return apperr.ErrNotFound
	case http.StatusConflict:
		return apperr.ErrConflict
	case http.StatusRequestEntityTooLarge:
		return apperr.ErrTooLarge
	}
	return nil
}

// Me returns the account and principal the key acts as.
func (c *Client) Me(ctx context.Context) (*account.MeResponse, error) {
	var out account.MeResponse
	return &out, c.do(ctx, http.MethodGet, "/api/v1/me", nil, nil, &out)
}

// ListAccounts lists the accounts the key can see.
func (c *Client) ListAccounts(ctx context.Context) ([]account.Account, error) {
	var out []account.Account
	return out, c.do(ctx, http.MethodGet, "/api/v1/accounts", nil, nil, &out)
}

// CreateAccount creates an account (admin key).
func (c *Client) CreateAccount(ctx context.Context, in account.CreateAccountInput) (*account.Account, error) {
	var out account.Account
	return &out, c.do(ctx, http.MethodPost, "/api/v1/accounts", nil, in, &out)
}

// DisableAccount disables an account and revokes its keys (admin key).
func (c *Client) DisableAccount(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/api/v1/accounts/"+url.PathEscape(id)+"/disable", nil, nil, nil)
}

// ListKeys lists an account's keys; an empty accountID means the caller's own.
func (c *Client) ListKeys(ctx context.Context, accountID string) ([]account.APIKey, error) {
	q := url.Values{}
	if accountID != "" {
		q.Set("account_id", accountID)
	}
	var out []account.APIKey
	return out, c.do(ctx, http.MethodGet, "/api/v1/keys", q, nil, &out)
}

// CreateKey issues a key. The returned secret is never shown again.
func (c *Client) CreateKey(ctx context.Context, in account.CreateKeyInput) (*account.IssuedKey, error) {
	var out account.IssuedKey
	return &out, c.do(ctx, http.MethodPost, "/api/v1/keys", nil, in, &out)
}

// RevokeKey revokes a key.
func (c *Client) RevokeKey(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/api/v1/keys/"+url.PathEscape(id), nil, nil, nil)
}

// ListDatasets lists the datasets the key can read.
func (c *Client) ListDatasets(ctx context.Context, f dataset.ListFilter) ([]dataset.Dataset, error) {
	q := url.Values{}
	if f.Scope != "" {
		q.Set("scope", string(f.Scope))
	}
	if f.Name != "" {
		q.Set("name", f.Name)
	}
	var out []dataset.Dataset
	return out, c.do(ctx, http.MethodGet, "/api/v1/datasets", q, nil, &out)
}

// CreateDataset creates a dataset.
func (c *Client) CreateDataset(ctx context.Context, in dataset.CreateInput) (*dataset.Dataset, error) {
	var out dataset.Dataset
	return &out, c.do(ctx, http.MethodPost, "/api/v1/datasets", nil, in, &out)
}

// GetDataset returns one dataset.
func (c *Client) GetDataset(ctx context.Context, id string) (*dataset.Dataset, error) {
	var out dataset.Dataset
	return &out, c.do(ctx, http.MethodGet, "/api/v1/datasets/"+url.PathEscape(id), nil, nil, &out)
}

// DeleteDataset deletes a dataset and its fixtures.
func (c *Client) DeleteDataset(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/api/v1/datasets/"+url.PathEscape(id), nil, nil, nil)
}

// FreezeDataset makes a dataset's contents read-only.
func (c *Client) FreezeDataset(ctx context.Context, id string) (*dataset.Dataset, error) {
	var out dataset.Dataset
	return &out, c.do(ctx, http.MethodPost, "/api/v1/datasets/"+url.PathEscape(id)+"/freeze", nil, nil, &out)
}

// Fork copies a dataset into a new workspace owned by the key's account.
func (c *Client) Fork(ctx context.Context, id string, in dataset.ForkInput) (*dataset.CopyResult, error) {
	var out dataset.CopyResult
	return &out, c.do(ctx, http.MethodPost, "/api/v1/datasets/"+url.PathEscape(id)+"/fork", nil, in, &out)
}

// Promote copies a dataset into the global library as a frozen version (admin key).
func (c *Client) Promote(ctx context.Context, id string, in dataset.PromoteInput) (*dataset.CopyResult, error) {
	var out dataset.CopyResult
	return &out, c.do(ctx, http.MethodPost, "/api/v1/datasets/"+url.PathEscape(id)+"/promote", nil, in, &out)
}

// ListFixtures lists a dataset's fixtures.
func (c *Client) ListFixtures(ctx context.Context, datasetID string, f fixture.ListFilter) ([]fixture.Fixture, error) {
	q := url.Values{}
	set := func(k, v string) {
		if v != "" {
			q.Set(k, v)
		}
	}
	set("kind", string(f.Kind))
	set("format", f.Format)
	set("scenario", f.Scenario)
	set("tag", f.Tag)
	set("review_state", string(f.ReviewState))
	set("record_schema", f.RecordSchema)
	set("person_id", f.PersonID)
	if f.Limit > 0 {
		q.Set("limit", fmt.Sprint(f.Limit))
	}
	if f.Offset > 0 {
		q.Set("offset", fmt.Sprint(f.Offset))
	}
	var out []fixture.Fixture
	return out, c.do(ctx, http.MethodGet, "/api/v1/datasets/"+url.PathEscape(datasetID)+"/fixtures", q, nil, &out)
}

// UploadInput is a fixture's metadata for Upload. Empty fields are inferred
// by the server.
type UploadInput struct {
	Name         string
	Kind         fixture.Kind
	Format       string
	MimeType     string
	Scenario     string
	Tags         []string
	Source       fixture.Source
	Generator    string
	RecordSchema string
}

// Upload streams content into a dataset as a new fixture.
func (c *Client) Upload(ctx context.Context, datasetID string, in UploadInput, content io.Reader) (*fixture.Fixture, error) {
	q := url.Values{}
	q.Set("name", in.Name)
	set := func(k, v string) {
		if v != "" {
			q.Set(k, v)
		}
	}
	set("kind", string(in.Kind))
	set("format", in.Format)
	set("mime_type", in.MimeType)
	set("scenario", in.Scenario)
	set("tags", strings.Join(in.Tags, ","))
	set("source", string(in.Source))
	set("generator", in.Generator)
	set("record_schema", in.RecordSchema)
	var out fixture.Fixture
	return &out, c.send(ctx, http.MethodPost, "/api/v1/datasets/"+url.PathEscape(datasetID)+"/fixtures", q, content, "application/octet-stream", &out)
}

// Download returns a fixture's content; the caller closes it.
func (c *Client) Download(ctx context.Context, id string) (io.ReadCloser, error) {
	resp, err := c.request(ctx, http.MethodGet, "/api/v1/fixtures/"+url.PathEscape(id)+"/content", nil, nil, "")
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// GetFixture returns a fixture's metadata.
func (c *Client) GetFixture(ctx context.Context, id string) (*fixture.Fixture, error) {
	var out fixture.Fixture
	return &out, c.do(ctx, http.MethodGet, "/api/v1/fixtures/"+url.PathEscape(id), nil, nil, &out)
}

// DeleteFixture removes a fixture.
func (c *Client) DeleteFixture(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/api/v1/fixtures/"+url.PathEscape(id), nil, nil, nil)
}

// Review approves or rejects a real-derived fixture (admin key).
func (c *Client) Review(ctx context.Context, id string, approve bool) (*fixture.Fixture, error) {
	var out fixture.Fixture
	return &out, c.do(ctx, http.MethodPost, "/api/v1/fixtures/"+url.PathEscape(id)+"/review", nil, fixture.ReviewInput{Approve: &approve}, &out)
}

// ListPersons lists people in the registry.
func (c *Client) ListPersons(ctx context.Context, f person.ListFilter) ([]person.Person, error) {
	q := url.Values{}
	if f.Company != "" {
		q.Set("company", f.Company)
	}
	if f.Limit > 0 {
		q.Set("limit", fmt.Sprint(f.Limit))
	}
	if f.Offset > 0 {
		q.Set("offset", fmt.Sprint(f.Offset))
	}
	var out []person.Person
	return out, c.do(ctx, http.MethodGet, "/api/v1/persons", q, nil, &out)
}

// GetPerson returns one person.
func (c *Client) GetPerson(ctx context.Context, id string) (*person.Person, error) {
	var out person.Person
	return &out, c.do(ctx, http.MethodGet, "/api/v1/persons/"+url.PathEscape(id), nil, nil, &out)
}

// GeneratePersons adds new people to the registry.
func (c *Client) GeneratePersons(ctx context.Context, in person.GenerateInput) (*person.GenerateResult, error) {
	var out person.GenerateResult
	return &out, c.do(ctx, http.MethodPost, "/api/v1/persons", nil, in, &out)
}

// RecordSchemas lists the record schemas the server generates.
func (c *Client) RecordSchemas(ctx context.Context) ([]synth.Schema, error) {
	var out []synth.Schema
	return out, c.do(ctx, http.MethodGet, "/api/v1/record-schemas", nil, nil, &out)
}

// Generate fills a dataset with synthetic fixtures.
func (c *Client) Generate(ctx context.Context, datasetID string, req generate.Request) (*generate.Result, error) {
	var out generate.Result
	return &out, c.do(ctx, http.MethodPost, "/api/v1/datasets/"+url.PathEscape(datasetID)+"/generate", nil, req, &out)
}

// IngestFile is one raw email to ingest; Open is called once, while uploading.
type IngestFile struct {
	Name string
	Open func() (io.ReadCloser, error)
}

// Ingest uploads raw emails to be anonymised into a quarantine dataset
// (admin key). The files stream as multipart/form-data.
func (c *Client) Ingest(ctx context.Context, datasetID string, req ingest.Request, files []IngestFile) (*ingest.Result, error) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		pw.CloseWithError(writeIngestForm(mw, req, files))
	}()
	var out ingest.Result
	err := c.send(ctx, http.MethodPost, "/api/v1/datasets/"+url.PathEscape(datasetID)+"/ingest", nil, pr, mw.FormDataContentType(), &out)
	_ = pr.Close()
	return &out, err
}

func writeIngestForm(mw *multipart.Writer, req ingest.Request, files []IngestFile) error {
	fields := [][2]string{{"profile", req.Profile}, {"batch", req.Batch}, {"dry_run", strconv.FormatBool(req.DryRun)}}
	for _, f := range fields {
		if f[1] == "" {
			continue
		}
		if err := mw.WriteField(f[0], f[1]); err != nil {
			return err
		}
	}
	for _, f := range files {
		w, err := mw.CreateFormFile("file", f.Name)
		if err != nil {
			return err
		}
		r, err := f.Open()
		if err != nil {
			return fmt.Errorf("opening %s: %w", f.Name, err)
		}
		_, err = io.Copy(w, r)
		_ = r.Close()
		if err != nil {
			return fmt.Errorf("reading %s: %w", f.Name, err)
		}
	}
	return mw.Close()
}

// Export writes an export archive of the datasets to w and returns its
// counts (datasets, fixtures, persons, blobs, skipped).
func (c *Client) Export(ctx context.Context, datasetIDs []string, w io.Writer) (map[string]int, error) {
	b, err := json.Marshal(exchange.ExportRequest{DatasetIDs: datasetIDs})
	if err != nil {
		return nil, err
	}
	resp, err := c.requestOn(ctx, c.stream, http.MethodPost, "/api/v1/export", nil, bytes.NewReader(b), "application/json")
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	counts := map[string]int{}
	for _, k := range []string{"datasets", "fixtures", "persons", "blobs", "skipped"} {
		counts[k], _ = strconv.Atoi(resp.Header.Get("X-Exemplar-" + k))
	}
	if _, err := io.Copy(w, resp.Body); err != nil {
		return nil, fmt.Errorf("receiving the archive: %w", err)
	}
	return counts, nil
}

// Import sends an export archive and returns what the server added.
func (c *Client) Import(ctx context.Context, archive io.Reader) (*exchange.ImportResult, error) {
	resp, err := c.requestOn(ctx, c.stream, http.MethodPost, "/api/v1/import", nil, archive, "application/gzip")
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var out exchange.ImportResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding the import result: %w", err)
	}
	return &out, nil
}

// Purge deletes every fixture from an ingest batch (admin key).
func (c *Client) Purge(ctx context.Context, batch string) (*fixture.PurgeResult, error) {
	var out fixture.PurgeResult
	return &out, c.do(ctx, http.MethodPost, "/api/v1/purge", nil, fixture.PurgeInput{IngestBatch: batch}, &out)
}

// ListAudit returns audit events (admin key).
func (c *Client) ListAudit(ctx context.Context, f audit.Filter) ([]audit.Event, error) {
	q := url.Values{}
	set := func(k, v string) {
		if v != "" {
			q.Set(k, v)
		}
	}
	set("account_id", f.AccountID)
	set("action", f.Action)
	set("target_type", f.TargetType)
	set("target_id", f.TargetID)
	if f.AfterSeq > 0 {
		q.Set("after_seq", fmt.Sprint(f.AfterSeq))
	}
	if f.Limit > 0 {
		q.Set("limit", fmt.Sprint(f.Limit))
	}
	var out []audit.Event
	return out, c.do(ctx, http.MethodGet, "/api/v1/audit", q, nil, &out)
}

// do sends an optional JSON body and decodes an optional JSON response.
func (c *Client) do(ctx context.Context, method, path string, q url.Values, in, out interface{}) error {
	var body io.Reader
	contentType := ""
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body, contentType = bytes.NewReader(b), "application/json"
	}
	return c.send(ctx, method, path, q, body, contentType, out)
}

func (c *Client) send(ctx context.Context, method, path string, q url.Values, body io.Reader, contentType string, out interface{}) error {
	resp, err := c.request(ctx, method, path, q, body, contentType)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if out == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding %s %s response: %w", method, path, err)
	}
	return nil
}

// request sends the request and turns any non-2xx response into an *Error.
func (c *Client) request(ctx context.Context, method, path string, q url.Values, body io.Reader, contentType string) (*http.Response, error) {
	return c.requestOn(ctx, c.http, method, path, q, body, contentType)
}

func (c *Client) requestOn(ctx context.Context, hc *http.Client, method, path string, q url.Values, body io.Reader, contentType string) (*http.Response, error) {
	u := *c.base
	u.Path = c.base.Path + path
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", c.key)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling %s: %w", c.base.Host, err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	defer func() { _ = resp.Body.Close() }()
	var errBody struct {
		Error string `json:"error"`
	}
	msg := resp.Status
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&errBody); err == nil && errBody.Error != "" {
		msg = errBody.Error
	}
	return nil, &Error{Status: resp.StatusCode, Message: msg}
}
