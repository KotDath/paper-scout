package arxiv

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const DefaultEndpoint = "https://export.arxiv.org/api/query"
const maxBody = 2 << 20

var ErrNotFound = errors.New("arXiv paper not found")

// Error deliberately excludes response bodies, request URLs and transport
// diagnostics. Attempts includes failed network calls, for run budgeting.
type Error struct {
	Code      string
	Status    int
	Retryable bool
	Attempts  int
}

func (e *Error) Error() string {
	return fmt.Sprintf("arXiv %s (status=%d attempts=%d)", e.Code, e.Status, e.Attempts)
}

type Client struct {
	db       *sql.DB
	endpoint string
	http     *http.Client
}

// New uses the application's migrated SQLite DB. Endpoint may be empty for
// production, an official HTTPS API URL, or a literal loopback HTTP fixture.
// Create clients once and share them; all instances share the network gate.
func New(db *sql.DB, endpoint string) (*Client, error) {
	if db == nil {
		return nil, errors.New("arXiv requires SQLite")
	}
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, errors.New("invalid arXiv API endpoint")
	}
	official := u.Scheme == "https" && (u.Host == "export.arxiv.org" || u.Host == "arxiv.org")
	ip := net.ParseIP(u.Hostname())
	fixture := u.Scheme == "http" && ip != nil && ip.IsLoopback()
	if (!official && !fixture) || u.Path != "/api/query" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return nil, errors.New("invalid arXiv API endpoint")
	}
	return &Client{db: db, endpoint: endpoint, http: &http.Client{
		Timeout: 20 * time.Second,
		// A redirect is never fetched. Source locations do not control requests.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

// SearchRequest.Query is an arXiv expression (for example
// all:memory AND all:agents), not a URL. Dates are optional inclusive UTC days.
// With no dates, the adapter never inserts an arbitrary publication period.
type SearchRequest struct {
	Query string `json:"query"`
	From  string `json:"from,omitempty"`
	To    string `json:"to,omitempty"`
	Limit int    `json:"limit"`
}

type Result struct {
	Papers    []Paper   `json:"papers"`
	CacheHit  bool      `json:"cache_hit"`
	FetchedAt time.Time `json:"fetched_at"`
	Attempts  int       `json:"attempts"`
}

func (c *Client) Search(ctx context.Context, input SearchRequest) (Result, error) {
	query := strings.TrimSpace(input.Query)
	if query == "" || len(query) > 2000 || strings.IndexFunc(query, unicode.IsControl) >= 0 {
		return Result{}, errors.New("arXiv query must be 1..2000 bytes without control characters")
	}
	limit := input.Limit
	if limit == 0 {
		limit = 10
	}
	if limit < 1 || limit > 10 {
		return Result{}, errors.New("arXiv limit must be 1..10")
	}
	from, to := "*", "*"
	for i, date := range []string{input.From, input.To} {
		if date == "" {
			continue
		}
		t, err := time.Parse("2006-01-02", date)
		if err != nil {
			return Result{}, errors.New("arXiv dates must be YYYY-MM-DD")
		}
		if i == 0 {
			from = t.Format("20060102") + "0000"
		} else {
			to = t.Format("20060102") + "2359"
		}
	}
	if input.From != "" && input.To != "" && input.From > input.To {
		return Result{}, errors.New("arXiv date range is reversed")
	}
	if from != "*" || to != "*" {
		query = "(" + query + ") AND submittedDate:[" + from + " TO " + to + "]"
	}
	params := url.Values{"search_query": {query}, "start": {"0"}, "max_results": {strconv.Itoa(limit)}, "sortBy": {"relevance"}, "sortOrder": {"descending"}}
	return c.query(ctx, params, limit, nil)
}

// Get resolves a versionless ID to the latest explicit version returned by
// arXiv, or fetches the exact requested version. It returns exactly one paper.
func (c *Client) Get(ctx context.Context, rawID string) (Result, error) {
	id, err := ParseID(rawID)
	if err != nil {
		return Result{}, err
	}
	result, err := c.query(ctx, url.Values{"id_list": {id.String()}, "max_results": {"1"}}, 1, &id)
	if err == nil && len(result.Papers) == 0 {
		return result, ErrNotFound
	}
	return result, err
}

func (c *Client) query(ctx context.Context, params url.Values, limit int, requested *Identifier) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	key := c.endpoint + "?" + params.Encode()
	if result, ok, err := c.cached(ctx, key); ok || err != nil {
		return result, err
	}
	if err := acquire(ctx); err != nil {
		return Result{}, err
	}
	defer func() { <-sharedGate }()
	// Collapse concurrent cache misses, including clients sharing the DB.
	if result, ok, err := c.cached(ctx, key); ok || err != nil {
		return result, err
	}
	for attempt := 1; attempt <= 3; attempt++ {
		if err := pause(ctx, time.Until(nextRequest)); err != nil {
			return Result{Attempts: attempt - 1}, err
		}
		body, failure, retryDelay := c.fetch(ctx, key)
		nextRequest = time.Now().Add(3 * time.Second)
		if retryDelay > 3*time.Second {
			nextRequest = time.Now().Add(retryDelay)
		}
		if ctx.Err() != nil {
			return Result{Attempts: attempt}, ctx.Err()
		}
		if failure != nil {
			failure.Attempts = attempt
			if !failure.Retryable || attempt == 3 || retryDelay > 30*time.Second {
				return Result{Attempts: attempt}, failure
			}
			continue
		}
		papers, err := parseAtom(body, limit)
		if err != nil {
			var provider *Error
			if errors.As(err, &provider) {
				provider.Attempts = attempt
			}
			return Result{Attempts: attempt}, err
		}
		if requested != nil && len(papers) > 0 && (papers[0].ArxivID != requested.ArxivID || (requested.Version != 0 && papers[0].Version != requested.Version)) {
			return Result{Attempts: attempt}, errors.New("arXiv returned a different article version")
		}
		result := Result{Papers: papers, FetchedAt: time.Now().UTC(), Attempts: attempt}
		if err := c.save(ctx, key, result); err != nil {
			return result, err
		}
		return result, nil
	}
	panic("unreachable")
}

func (c *Client) fetch(ctx context.Context, address string) ([]byte, *Error, time.Duration) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, &Error{Code: "invalid_request"}, 0
	}
	req.Header.Set("User-Agent", "PaperScout/0.1 (local research prototype)")
	req.Header.Set("Accept", "application/atom+xml")
	response, err := c.http.Do(req)
	if err != nil {
		return nil, &Error{Code: "source_unavailable", Retryable: true}, 0
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		retryable := response.StatusCode == 429 || response.StatusCode >= 500
		delay := time.Duration(0)
		if header := response.Header.Get("Retry-After"); retryable && header != "" {
			if seconds, err := strconv.ParseInt(header, 10, 32); err == nil && seconds >= 0 {
				delay = time.Duration(seconds) * time.Second
			} else if date, err := http.ParseTime(header); err == nil {
				delay = time.Until(date)
			}
		}
		return nil, &Error{Code: "source_unavailable", Status: response.StatusCode, Retryable: retryable}, delay
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil {
		return nil, &Error{Code: "source_unavailable", Retryable: true}, 0
	}
	if len(body) > maxBody {
		return nil, &Error{Code: "response_too_large"}, 0
	}
	return body, nil, 0
}
