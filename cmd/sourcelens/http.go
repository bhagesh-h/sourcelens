package main

// HTTP client with per-host spacing and retry / back-off, mirroring
// src/sourcelens/common/agelit.py:Http.

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A server that asks for a longer wait than this gets no more requests from
// the step; the step then fails with a note, and the next run tries again.
const maxRetryWait = 600 * time.Second

var (
	rateMu      sync.Mutex
	rateLimited = map[string]time.Time{} // host -> when it accepts requests again
)

func waitText(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%.0f min", d.Minutes())
	}
	return fmt.Sprintf("%.1f h", d.Hours())
}

// rateLimitNotes: one line per host that asked for a long wait, for the end of a step's log.
func rateLimitNotes() []string {
	rateMu.Lock()
	defer rateMu.Unlock()
	hosts := make([]string, 0, len(rateLimited))
	for h := range rateLimited {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	var notes []string
	for _, h := range hosts {
		note := fmt.Sprintf("rate limited by %s until %s; run again then", h, rateLimited[h].Format("2006-01-02 15:04"))
		if h == "api.openalex.org" {
			note += ", or set an OpenAlex API key (sourcelens config)"
		}
		notes = append(notes, note)
	}
	return notes
}

// urlHost is the third "/"-separated part of a URL, as python url.split("/")[2].
func urlHost(rawurl string) string {
	parts := strings.SplitN(rawurl, "/", 4)
	if len(parts) < 3 {
		return ""
	}
	return parts[2]
}

// contact: contact email from the settings (or SOURCELENS_EMAIL); empty means
// no polite-pool address, and Unpaywall is skipped.
func contact() string { return strings.TrimSpace(os.Getenv("CONTACT_EMAIL")) }
func userAgent() string {
	if contact() == "" {
		return "sourcelens/" + version + " (research literature catalogue)"
	}
	return "sourcelens/" + version + " (research literature catalogue; mailto:" + contact() + ")"
}

// Limiter spaces requests per host; shared limiters serve many goroutines.
type Limiter struct {
	mu       sync.Mutex
	next     map[string]time.Time
	interval map[string]time.Duration
	def      time.Duration
}

func NewLimiter(def time.Duration, per map[string]time.Duration) *Limiter {
	return &Limiter{next: map[string]time.Time{}, interval: per, def: def}
}

func (l *Limiter) Wait(rawurl string) {
	host := ""
	if u, err := url.Parse(rawurl); err == nil {
		host = strings.ToLower(u.Host)
	}
	gap := l.def
	if g, ok := l.interval[host]; ok {
		gap = g
	}
	l.mu.Lock()
	now := time.Now()
	slot := l.next[host]
	if slot.Before(now) {
		slot = now
	}
	l.next[host] = slot.Add(gap)
	l.mu.Unlock()
	if d := time.Until(slot); d > 0 {
		time.Sleep(d)
	}
}

type Resp struct {
	Status int
	Body   []byte
	URL    string
	Header http.Header
}

func (r *Resp) Text() string { return string(r.Body) }

type Client struct {
	c       *http.Client
	lim     *Limiter
	headers map[string]string
	log     *Logger
}

func NewClient(interval time.Duration, headers map[string]string, log *Logger) *Client {
	return &Client{c: &http.Client{Timeout: 90 * time.Second}, lim: NewLimiter(interval, nil),
		headers: headers, log: log}
}

func (c *Client) WithLimiter(l *Limiter) *Client { c.lim = l; return c }

type reqOpts struct {
	params  url.Values
	form    url.Values
	timeout time.Duration
	tries   int
	no404   bool // a 404 is returned instead of nil (python allow_404=False)
	raw     bool // one plain request: any status is returned (python session.get / session.post)
}

func (c *Client) do(method, rawurl string, o reqOpts) (*Resp, error) {
	if o.tries == 0 {
		o.tries = 6
	}
	if o.params != nil {
		sep := "?"
		if strings.Contains(rawurl, "?") {
			sep = "&"
		}
		rawurl += sep + o.params.Encode()
	}
	host := urlHost(rawurl)
	if method == "GET" && !o.raw {
		rateMu.Lock()
		_, blocked := rateLimited[host]
		rateMu.Unlock()
		if blocked {
			return nil, nil
		}
	}
	delay := 2 * time.Second
	var lastErr error
	for attempt := 1; attempt <= o.tries; attempt++ {
		c.lim.Wait(rawurl)
		var body io.Reader
		if o.form != nil {
			body = strings.NewReader(o.form.Encode())
		}
		req, err := http.NewRequest(method, rawurl, body)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent())
		req.Header.Set("Accept", "*/*") // as python requests; some CDNs refuse requests without it
		for k, v := range c.headers {
			req.Header.Set(k, v)
		}
		if o.form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		cl := c.c
		if o.timeout > 0 {
			cl = &http.Client{Timeout: o.timeout}
		}
		res, err := cl.Do(req)
		if err != nil {
			lastErr = err
			if c.log != nil {
				c.log.Printf("%s error %v on %s (try %d)", method, shortErr(err), cut140(rawurl), attempt)
			}
			if attempt == o.tries {
				break
			}
			time.Sleep(delay)
			delay = minDur(delay*2, 120*time.Second)
			continue
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		r := &Resp{Status: res.StatusCode, Body: b, URL: res.Request.URL.String(), Header: res.Header}
		if res.StatusCode == 200 || o.raw {
			return r, nil
		}
		if method == "POST" { // python Http.post: every non-200 is retried, then None
			if c.log != nil {
				c.log.Printf("POST %d on %s (try %d)", res.StatusCode, cut140(rawurl), attempt)
			}
			time.Sleep(delay)
			delay = minDur(delay*2, 120*time.Second)
			continue
		}
		if res.StatusCode == 404 && !o.no404 {
			return nil, nil
		}
		switch res.StatusCode {
		case 401, 403, 404, 410, 451:
			return r, nil
		}
		if attempt == o.tries { // python: no sleep after the last try, then None
			break
		}
		wait := delay
		if ra := res.Header.Get("Retry-After"); ra != "" {
			if s, err := strconv.Atoi(ra); err == nil && time.Duration(s)*time.Second > wait {
				wait = time.Duration(s) * time.Second
			}
		}
		if wait > maxRetryWait {
			rateMu.Lock()
			rateLimited[host] = time.Now().Add(wait)
			rateMu.Unlock()
			if c.log != nil {
				c.log.Printf("%s %d on %s: asked to wait %s; no more requests to %s", method, res.StatusCode, cut140(rawurl), waitText(wait), host)
			}
			return nil, nil
		}
		if c.log != nil {
			c.log.Printf("%s %d on %s (try %d); waiting %.0fs", method, res.StatusCode, cut140(rawurl), attempt, wait.Seconds())
		}
		time.Sleep(wait)
		delay = minDur(delay*2, 120*time.Second)
	}
	return nil, lastErr
}

// Get mirrors Http.get: 200 -> response; 404 -> nil unless no404; 401/403/404/410/451 -> response;
// other statuses are retried and give nil after the last try.
func (c *Client) Get(rawurl string, o reqOpts) *Resp {
	r, _ := c.do("GET", rawurl, o)
	return r
}

func (c *Client) Post(rawurl string, form url.Values, o reqOpts) *Resp {
	o.form = form
	r, _ := c.do("POST", rawurl, o)
	return r
}

func shortErr(err error) string {
	s := err.Error()
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

func cut140(s string) string {
	if len(s) > 140 {
		return s[:140]
	}
	return s
}

func minDur(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
