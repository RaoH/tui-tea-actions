package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

type Client struct {
	base  string
	token string
	http  *http.Client
}

func NewClient(base, token string) *Client {
	return &Client{
		base:  strings.TrimRight(base, "/"),
		token: token,
		http:  &http.Client{Timeout: 20 * time.Second},
	}
}

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	u := c.base + "/api/v1" + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "token "+c.token)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type Repo struct {
	FullName   string `json:"full_name"`
	HTMLURL    string `json:"html_url"`
	HasActions bool   `json:"has_actions"`
	Archived   bool   `json:"archived"`
}

// Repos returns every repo visible to the token that has Actions enabled.
func (c *Client) Repos(ctx context.Context) ([]Repo, error) {
	const limit = 50
	var all []Repo
	for page := 1; ; page++ {
		var res struct {
			Data []Repo `json:"data"`
		}
		q := url.Values{"limit": {fmt.Sprint(limit)}, "page": {fmt.Sprint(page)}}
		if err := c.get(ctx, "/repos/search", q, &res); err != nil {
			return nil, err
		}
		for _, r := range res.Data {
			if r.HasActions && !r.Archived {
				all = append(all, r)
			}
		}
		if len(res.Data) < limit {
			return all, nil
		}
	}
}

// apiTime tolerates empty strings, which time.Time's decoder rejects.
type apiTime struct{ time.Time }

func (t *apiTime) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil || s == "" {
		return nil
	}
	p, err := time.Parse(time.RFC3339, s)
	if err == nil && p.Unix() > 0 { // Forgejo uses the Unix epoch for "never"
		t.Time = p
	}
	return nil
}

type Run struct {
	ID          int64   `json:"id"`
	Title       string  `json:"title"`
	WorkflowID  string  `json:"workflow_id"`
	Index       int64   `json:"index_in_repo"`
	Ref         string  `json:"prettyref"`
	Event       string  `json:"trigger_event"`
	Status      string  `json:"status"`
	CommitSHA   string  `json:"commit_sha"`
	HTMLURL     string  `json:"html_url"`
	Created     apiTime `json:"created"`
	Started     apiTime `json:"started"`
	Stopped     apiTime `json:"stopped"`
	TriggerUser *struct {
		Login string `json:"login"`
	} `json:"trigger_user"`

	Repo string `json:"-"`
}

func (r Run) Active() bool {
	switch r.Status {
	case "running", "waiting", "blocked":
		return true
	}
	return false
}

func (r Run) Elapsed() time.Duration {
	if r.Started.IsZero() {
		return 0
	}
	end := r.Stopped.Time
	if end.IsZero() || r.Active() {
		end = time.Now()
	}
	return end.Sub(r.Started.Time)
}

func (c *Client) Runs(ctx context.Context, repo string, limit int) ([]Run, error) {
	var res struct {
		Runs []Run `json:"workflow_runs"`
	}
	// Forgejo ignores limit unless page is also set.
	q := url.Values{"limit": {fmt.Sprint(limit)}, "page": {"1"}}
	if err := c.get(ctx, "/repos/"+repo+"/actions/runs", q, &res); err != nil {
		return nil, err
	}
	for i := range res.Runs {
		res.Runs[i].Repo = repo
	}
	return res.Runs, nil
}

// Me returns the login of the token's user.
func (c *Client) Me(ctx context.Context) (string, error) {
	var u struct {
		Login string `json:"login"`
	}
	err := c.get(ctx, "/user", nil, &u)
	return u.Login, err
}

func (r Run) TriggeredBy() string {
	if r.TriggerUser == nil {
		return ""
	}
	return r.TriggerUser.Login
}

// Run fetches a single run by its instance-wide ID.
func (c *Client) Run(ctx context.Context, repo string, id int64) (Run, error) {
	var r Run
	if err := c.get(ctx, fmt.Sprintf("/repos/%s/actions/runs/%d", repo, id), nil, &r); err != nil {
		return r, err
	}
	r.Repo = repo
	return r, nil
}

// AllRuns fetches the latest runs for each repo concurrently and returns them
// newest first. Per-repo failures are returned as a joined warning, not an error.
func (c *Client) AllRuns(ctx context.Context, repos []string, limit int) ([]Run, string) {
	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		runs []Run
		errs []string
		sem  = make(chan struct{}, 8)
	)
	for _, repo := range repos {
		wg.Add(1)
		go func(repo string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rs, err := c.Runs(ctx, repo, limit)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, repo)
				return
			}
			runs = append(runs, rs...)
		}(repo)
	}
	wg.Wait()
	sort.SliceStable(runs, func(i, j int) bool {
		return runs[i].Created.After(runs[j].Created.Time)
	})
	warn := ""
	if len(errs) > 0 {
		sort.Strings(errs)
		warn = "failed to fetch: " + strings.Join(errs, ", ")
	}
	return runs, warn
}
