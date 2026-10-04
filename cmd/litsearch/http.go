package main

// HTTP client with per-host spacing and retry / back-off, mirroring
// src/litsearch/common/agelit.py:Http.

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// contact: contact email from the settings (or LITSEARCH_EMAIL); empty means
// no polite-pool address, and Unpaywall is skipped.
func contact() string { return strings.TrimSpace(os.Getenv("CONTACT_EMAIL")) }
func userAgent() string {
	if contact() == "" {
		return "litsearch/" + version + " (research literature catalogue)"
	}
	return "litsearch/" + version + " (research literature catalogue; mailto:" + contact() + ")"
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
