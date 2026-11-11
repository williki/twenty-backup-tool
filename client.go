package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	rateWindow  = time.Minute
	maxRequests = 90 // stay under server's 100/min cap
)

type TwentyClient struct {
	BaseURL string
	apiKey  string
	http    *http.Client

	mu         sync.Mutex
	timestamps []time.Time
}

func NewTwentyClient(baseURL, apiKey string) *TwentyClient {
	tr := &http.Transport{}
	return &TwentyClient{
		BaseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    &http.Client{Transport: tr, Timeout: 0},
	}
}

// throttle enforces the sliding-window rate limit. Returns after the next
// request slot is available and records its timestamp.
func (c *TwentyClient) throttle() {
	c.mu.Lock()
	now := time.Now()
	cutoff := now.Add(-rateWindow)
	i := 0
	for ; i < len(c.timestamps) && c.timestamps[i].Before(cutoff); i++ {
	}
	c.timestamps = c.timestamps[i:]

	if len(c.timestamps) >= maxRequests {
		wait := rateWindow - now.Sub(c.timestamps[0]) + 100*time.Millisecond
		c.mu.Unlock()
		fmt.Fprintf(os.Stderr, "[rate-limit] waiting %ds…\n", int(wait.Seconds())+1)
		time.Sleep(wait)
		c.mu.Lock()
	}
	c.timestamps = append(c.timestamps, time.Now())
	c.mu.Unlock()
}

// do performs the request, retrying on 429 using the retry-after header.
// The response body is fully read and returned; out (if non-nil) is JSON-decoded.
func (c *TwentyClient) do(method, path string, body []byte, out any) error {
	for {
		c.throttle()

		req, err := http.NewRequest(method, c.BaseURL+path, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		req.Header.Set("Content-Type", "application/json")

		res, err := c.http.Do(req)
		if err != nil {
			// Unwrap so the user sees the underlying cause (DNS, TLS, etc.)
			var urlErr *url.Error
			detail := err.Error()
			if errors.As(err, &urlErr) && urlErr.Err != nil {
				detail = urlErr.Err.Error()
			}
			return fmt.Errorf("network error reaching %s: %s", c.BaseURL+path, detail)
		}

		if res.StatusCode == http.StatusTooManyRequests {
			retryAfter := 60
			if v := res.Header.Get("retry-after"); v != "" {
				if n, err := strconv.Atoi(v); err == nil {
					retryAfter = n
				}
			}
			res.Body.Close()
			fmt.Fprintf(os.Stderr, "[429] retry after %ds\n", retryAfter)
			time.Sleep(time.Duration(retryAfter) * time.Second)
			continue
		}

		respBody, readErr := io.ReadAll(res.Body)
		res.Body.Close()
		if readErr != nil {
			return fmt.Errorf("reading response: %w", readErr)
		}

		if res.StatusCode < 200 || res.StatusCode >= 300 {
			return fmt.Errorf("HTTP %d %s — %s\n%s", res.StatusCode, res.Status, path, string(respBody))
		}

		if out != nil {
			if err := json.Unmarshal(respBody, out); err != nil {
				return fmt.Errorf("decoding response from %s: %w", path, err)
			}
		}
		return nil
	}
}

func (c *TwentyClient) Get(path string, out any) error {
	return c.do("GET", path, nil, out)
}

func (c *TwentyClient) Post(path string, body []byte, out any) error {
	return c.do("POST", path, body, out)
}

type graphqlEnvelope struct {
	Data   json.RawMessage `json:"data"`
	Errors []struct {
		Message    string          `json:"message"`
		Extensions json.RawMessage `json:"extensions,omitempty"`
	} `json:"errors"`
}

func (c *TwentyClient) graphqlCall(endpoint, query string, variables map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return err
	}
	var env graphqlEnvelope
	if err := c.Post(endpoint, body, &env); err != nil {
		return err
	}
	if len(env.Errors) > 0 {
		msgs := make([]string, len(env.Errors))
		for i, e := range env.Errors {
			msgs[i] = e.Message
			if len(e.Extensions) > 0 && string(e.Extensions) != "null" {
				msgs[i] += " | extensions: " + string(e.Extensions)
			}
		}
		label := "GraphQL"
		if endpoint == "/metadata" {
			label = "Metadata GraphQL"
		}
		return fmt.Errorf("%s: %s", label, strings.Join(msgs, "; "))
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return fmt.Errorf("GraphQL returned no data field")
	}
	if out != nil {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("decoding GraphQL data: %w", err)
		}
	}
	return nil
}

// GraphQL calls the core /graphql endpoint.
func (c *TwentyClient) GraphQL(query string, variables map[string]any, out any) error {
	return c.graphqlCall("/graphql", query, variables, out)
}

// MetaGraphQL calls the metadata /metadata endpoint.
func (c *TwentyClient) MetaGraphQL(query string, variables map[string]any, out any) error {
	return c.graphqlCall("/metadata", query, variables, out)
}
