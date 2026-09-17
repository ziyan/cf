// Package client talks to the Confluence Cloud REST API.
package client

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ziyan/cf/internal/config"
)

const (
	// maximumIdleConnections is how many connections to the site the client
	// keeps open. The default of two is not enough for a sync reading several
	// things at once, which would otherwise pay a TLS handshake per request.
	maximumIdleConnections = 32

	// responseHeaderTimeout bounds the wait for a server to begin answering.
	responseHeaderTimeout = 2 * time.Minute

	// requestTimeout bounds one request from asking to the last byte of the
	// answer. A connection that goes quiet without closing would otherwise
	// stop a sync for good, and the header timeout above does not cover
	// reading the body.
	requestTimeout = 5 * time.Minute

	retryCount = 5
)

// Client is one authenticated Confluence site.
type Client struct {
	baseURL       string
	authorization string
	httpClient    *http.Client
}

// New builds a client for a profile.
func New(profile *config.Profile) *Client {
	credentials := base64.StdEncoding.EncodeToString([]byte(profile.Email + ":" + profile.Token))
	return &Client{
		baseURL:       profile.BaseURL(),
		authorization: "Basic " + credentials,
		httpClient:    &http.Client{Transport: newTransport()},
	}
}

// BaseURL is the site's root, which a permalink is built from.
func (self *Client) BaseURL() string {
	return self.baseURL
}

// newTransport is the standard transport, kept warm for several callers at
// once.
//
// It speaks HTTP/1.1. Over HTTP/2 every request to a site shares one
// connection, and a connection that stops answering without closing takes
// every request on it down with it, with no way to notice: the timeout that
// would catch it is not honoured on an HTTP/2 stream, and the setting that
// would ping the connection does nothing in this version of Go.
func newTransport() http.RoundTripper {
	transport, isStandard := http.DefaultTransport.(*http.Transport)
	if !isStandard {
		return http.DefaultTransport
	}
	cloned := transport.Clone()
	cloned.MaxIdleConns = maximumIdleConnections * 2
	cloned.MaxIdleConnsPerHost = maximumIdleConnections
	cloned.ResponseHeaderTimeout = responseHeaderTimeout
	cloned.ForceAttemptHTTP2 = false
	// Turning off the HTTP/2 handler is not enough on its own. The protocol
	// is settled in the TLS handshake, so a client still offering h2 there
	// gets h2 frames back and cannot read them.
	cloned.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	if cloned.TLSClientConfig == nil {
		cloned.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	cloned.TLSClientConfig.NextProtos = []string{"http/1.1"}
	return cloned
}

// Get reads one path, which may be absolute or relative to the site, and
// decodes the JSON into target.
func (self *Client) Get(ctx context.Context, path string, target interface{}) error {
	body, err := self.GetRaw(ctx, path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("client: parsing %s: %w", path, err)
	}
	return nil
}

// GetRaw reads one path and hands back the bytes.
//
// A 4xx other than 408 or 429 is the site saying no, and asking again with the
// same request and the same token will not change its mind, so it is returned
// as is. Everything else is tried again, with a deadline on each attempt.
func (self *Client) GetRaw(ctx context.Context, path string) ([]byte, error) {
	address := path
	if !strings.HasPrefix(address, "http") {
		address = self.baseURL + path
	}

	var lastError error
	for attempt := 0; attempt < retryCount; attempt++ {
		body, status, err := self.getOnce(ctx, address)
		if err == nil {
			return body, nil
		}
		if status >= http.StatusBadRequest && status < http.StatusInternalServerError &&
			status != http.StatusTooManyRequests && status != http.StatusRequestTimeout {
			return nil, err
		}
		lastError = err
		if attempt < retryCount-1 {
			time.Sleep(time.Duration(2*(attempt+1)) * time.Second)
		}
	}
	return nil, lastError
}

func (self *Client) getOnce(ctx context.Context, address string) ([]byte, int, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(attemptCtx, http.MethodGet, address, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("client: building a request for %s: %w", address, err)
	}
	request.Header.Set("Authorization", self.authorization)
	request.Header.Set("Accept", "application/json")

	response, err := self.httpClient.Do(request)
	if err != nil {
		return nil, 0, fmt.Errorf("client: reading %s: %w", address, err)
	}
	defer func() { _ = response.Body.Close() }()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, response.StatusCode, fmt.Errorf("client: reading %s: %w", address, err)
	}
	if response.StatusCode >= http.StatusBadRequest {
		return nil, response.StatusCode, fmt.Errorf("client: reading %s: %s", address, summarize(body, response.StatusCode))
	}
	return body, response.StatusCode, nil
}

// summarize turns an error response into one line, without repeating a whole
// HTML error page into the log.
func summarize(body []byte, status int) string {
	message := struct {
		Message string `json:"message"`
		Errors  []struct {
			Title string `json:"title"`
		} `json:"errors"`
	}{}
	if json.Unmarshal(body, &message) == nil {
		if message.Message != "" {
			return fmt.Sprintf("%d %s", status, message.Message)
		}
		if len(message.Errors) > 0 && message.Errors[0].Title != "" {
			return fmt.Sprintf("%d %s", status, message.Errors[0].Title)
		}
	}
	return fmt.Sprintf("%d %s", status, http.StatusText(status))
}

// Query builds a path with the parameters escaped.
func Query(path string, parameters map[string]string) string {
	values := url.Values{}
	for key, value := range parameters {
		if value != "" {
			values.Set(key, value)
		}
	}
	if len(values) == 0 {
		return path
	}
	return path + "?" + values.Encode()
}
