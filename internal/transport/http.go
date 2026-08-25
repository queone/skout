// Package transport provides validated, bounded HTTP execution for providers.
package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Method is an HTTP method supported by the provider slice.
type Method string

const (
	Get  Method = http.MethodGet
	Head Method = http.MethodHead
	Post Method = http.MethodPost
)

// Header is one duplicate-safe HTTP header.
type Header struct {
	Name  string
	Value string
}

func (header Header) String() string {
	value := header.Value
	if sensitiveHeader(header.Name) {
		value = "<redacted>"
	}
	return header.Name + ": " + value
}

// Request is one provider-neutral request awaiting validation.
type Request struct {
	Method    Method
	URL       string
	Headers   []Header
	Body      []byte
	Timeout   time.Duration
	BodyLimit int64
}

// Response is one complete bounded response.
type Response struct {
	Status  int
	Headers []Header
	Body    []byte
}

// ValidatedRequest is exposed to injected executors only after validation.
type ValidatedRequest struct {
	method    Method
	url       *url.URL
	headers   []Header
	body      []byte
	timeout   time.Duration
	bodyLimit int64
}

func (request ValidatedRequest) Method() Method         { return request.method }
func (request ValidatedRequest) URL() string            { return request.url.String() }
func (request ValidatedRequest) Headers() []Header      { return append([]Header(nil), request.headers...) }
func (request ValidatedRequest) Body() []byte           { return append([]byte(nil), request.body...) }
func (request ValidatedRequest) Timeout() time.Duration { return request.timeout }
func (request ValidatedRequest) BodyLimit() int64       { return request.bodyLimit }

// Executor runs a request that has already passed validation.
type Executor interface {
	Execute(ValidatedRequest) (Response, error)
}

// Client validates requests before dispatch.
type Client struct {
	executor Executor
}

// New constructs a client around an injected executor.
func New(executor Executor) *Client { return &Client{executor: executor} }

// Production constructs the bounded production HTTPS client.
func Production() *Client { return New(newHTTPExecutor()) }

// Execute validates and executes one request.
func (client *Client) Execute(request Request) (Response, error) {
	validated, err := validate(request)
	if err != nil {
		return Response{}, err
	}
	response, err := client.executor.Execute(validated)
	if err != nil {
		return Response{}, err
	}
	return response, nil
}

func validate(request Request) (ValidatedRequest, error) {
	if request.Timeout <= 0 {
		return ValidatedRequest{}, invalid("timeout must be positive")
	}
	if request.BodyLimit <= 0 {
		return ValidatedRequest{}, invalid("body limit must be positive")
	}
	if request.Method != Get && request.Method != Head && request.Method != Post {
		return ValidatedRequest{}, invalid("method must be GET, HEAD, or POST")
	}
	if request.Method != Post && len(request.Body) != 0 {
		return ValidatedRequest{}, invalid("only POST requests may contain a body")
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || parsed.Host == "" {
		return ValidatedRequest{}, invalid("URL is invalid")
	}
	if err := validateInitialURL(parsed); err != nil {
		return ValidatedRequest{}, err
	}
	headers := make([]Header, 0, len(request.Headers))
	for _, header := range request.Headers {
		if !validHeaderName(header.Name) {
			return ValidatedRequest{}, invalid("header name is invalid")
		}
		if !validHeaderValue(header.Value) {
			return ValidatedRequest{}, invalid(fmt.Sprintf("header %q contains invalid characters", header.Name))
		}
		headers = append(headers, Header{Name: textproto.CanonicalMIMEHeaderKey(header.Name), Value: header.Value})
	}
	return ValidatedRequest{
		method:    request.Method,
		url:       parsed,
		headers:   headers,
		body:      append([]byte(nil), request.Body...),
		timeout:   request.Timeout,
		bodyLimit: request.BodyLimit,
	}, nil
}

type httpExecutor struct {
	client *http.Client
}

func newHTTPExecutor() *httpExecutor {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableCompression = true
	return &httpExecutor{client: &http.Client{
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

func (executor *httpExecutor) Execute(request ValidatedRequest) (Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), request.timeout)
	defer cancel()
	currentURL := cloneURL(request.url)
	headers := append([]Header(nil), request.headers...)
	visited := map[string]struct{}{currentURL.String(): {}}
	for redirects := 0; ; {
		response, err := executor.dispatch(ctx, request.method, currentURL, headers, request.body)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return Response{}, fmt.Errorf("dispatch HTTP request to %s: total timeout expired; retry when the provider is responsive", redactURL(currentURL))
			}
			return Response{}, fmt.Errorf("dispatch HTTP request to %s: %s; check connectivity and TLS configuration, then retry", redactURL(currentURL), safeExecutionError(err))
		}
		if redirectStatus(response.StatusCode) && (request.method == Get || request.method == Head) {
			location := response.Header.Get("Location")
			if location != "" {
				_ = response.Body.Close()
				if redirects == 10 {
					return Response{}, fmt.Errorf("follow HTTP redirect: redirect limit of ten was exceeded; verify the provider endpoint and retry")
				}
				next, err := currentURL.Parse(location)
				if err != nil {
					return Response{}, fmt.Errorf("follow HTTP redirect: invalid Location header; verify the provider endpoint and retry")
				}
				if err := validateRedirectURL(currentURL, next); err != nil {
					return Response{}, err
				}
				if _, exists := visited[next.String()]; exists {
					return Response{}, fmt.Errorf("follow HTTP redirect: redirect loop detected; verify the provider endpoint and retry")
				}
				visited[next.String()] = struct{}{}
				if origin(currentURL) != origin(next) {
					headers = stripRedirectSensitive(headers)
				}
				currentURL = next
				redirects++
				continue
			}
		}
		return readResponse(response, request.bodyLimit)
	}
}

func safeExecutionError(err error) string {
	return "request execution failed"
}

func (executor *httpExecutor) dispatch(ctx context.Context, method Method, target *url.URL, headers []Header, body []byte) (*http.Response, error) {
	var reader io.Reader
	if method == Post {
		reader = strings.NewReader(string(body))
	}
	request, err := http.NewRequestWithContext(ctx, string(method), target.String(), reader)
	if err != nil {
		return nil, err
	}
	hasUserAgent := false
	for _, header := range headers {
		request.Header.Add(header.Name, header.Value)
		hasUserAgent = hasUserAgent || strings.EqualFold(header.Name, "User-Agent")
	}
	if !hasUserAgent {
		request.Header["User-Agent"] = []string{""}
	}
	return executor.client.Do(request)
}

func readResponse(response *http.Response, limit int64) (Response, error) {
	defer response.Body.Close()
	if response.ContentLength > limit {
		return Response{}, fmt.Errorf("read HTTP response: body exceeds %d bytes; raise the provider-specific limit only after verifying the response", limit)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return Response{}, fmt.Errorf("read HTTP response: response body read failed; retry when the provider is responsive")
	}
	if int64(len(body)) > limit {
		return Response{}, fmt.Errorf("read HTTP response: body exceeds %d bytes; raise the provider-specific limit only after verifying the response", limit)
	}
	names := make([]string, 0, len(response.Header))
	for name := range response.Header {
		names = append(names, strings.ToLower(name))
	}
	sort.Strings(names)
	headers := make([]Header, 0, len(names))
	for _, lowerName := range names {
		for originalName, values := range response.Header {
			if strings.EqualFold(originalName, lowerName) {
				for _, value := range values {
					if !utf8.ValidString(value) {
						value = "<non-text>"
					}
					headers = append(headers, Header{Name: lowerName, Value: value})
				}
				break
			}
		}
	}
	return Response{Status: response.StatusCode, Headers: headers, Body: body}, nil
}

func validateInitialURL(target *url.URL) error {
	if target.User != nil {
		return invalid("URL credentials are prohibited")
	}
	if target.Fragment != "" {
		return invalid("URL fragments are prohibited")
	}
	switch target.Scheme {
	case "https":
		return nil
	case "http":
		if loopback(target) {
			return nil
		}
		return invalid("HTTP is permitted only for loopback fixtures")
	default:
		return invalid("URL scheme must be HTTPS or loopback HTTP")
	}
}

func validateRedirectURL(previous, next *url.URL) error {
	if next.User != nil || next.Fragment != "" {
		return fmt.Errorf("follow HTTP redirect: redirect target contains credentials or a fragment; verify the provider endpoint and retry")
	}
	if previous.Scheme == "https" && next.Scheme == "http" {
		return fmt.Errorf("follow HTTP redirect: HTTPS-to-HTTP downgrade is prohibited; verify the provider endpoint and retry")
	}
	if next.Scheme == "https" || next.Scheme == "http" && loopback(previous) && loopback(next) {
		return nil
	}
	return fmt.Errorf("follow HTTP redirect: unsupported or non-loopback redirect scheme; verify the provider endpoint and retry")
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, character := range []byte(name) {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(character))) {
			return false
		}
	}
	return true
}

func validHeaderValue(value string) bool {
	for _, character := range []byte(value) {
		if character == '\r' || character == '\n' || character == 0x7f || character < 0x20 && character != '\t' {
			return false
		}
	}
	return true
}

func loopback(target *url.URL) bool {
	host := target.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func origin(target *url.URL) string {
	port := target.Port()
	if port == "" {
		if target.Scheme == "https" {
			port = "443"
		} else if target.Scheme == "http" {
			port = "80"
		}
	}
	return strings.ToLower(target.Scheme) + "://" + strings.ToLower(target.Hostname()) + ":" + port
}

func redirectStatus(status int) bool {
	return status >= 300 && status < 400
}

func stripRedirectSensitive(headers []Header) []Header {
	filtered := headers[:0]
	for _, header := range headers {
		if !redirectSensitive(header.Name) {
			filtered = append(filtered, header)
		}
	}
	return filtered
}

func redirectSensitive(name string) bool {
	switch strings.ToLower(name) {
	case "authorization", "proxy-authorization", "cookie", "cookie2":
		return true
	default:
		return false
	}
}

func sensitiveHeader(name string) bool {
	lower := strings.ToLower(name)
	return redirectSensitive(lower) || strings.Contains(lower, "token") || strings.Contains(lower, "api-key") || strings.Contains(lower, "apikey") || strings.Contains(lower, "secret")
}

func redactURL(target *url.URL) string {
	copy := cloneURL(target)
	query := copy.Query()
	for key, values := range query {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "token") || strings.Contains(lower, "key") || strings.Contains(lower, "secret") || strings.Contains(lower, "auth") {
			for index := range values {
				values[index] = "<redacted>"
			}
			query[key] = values
		}
	}
	copy.RawQuery = query.Encode()
	return copy.String()
}

func cloneURL(target *url.URL) *url.URL {
	copy := *target
	return &copy
}

func invalid(detail string) error {
	return fmt.Errorf("validate HTTP request: %s; correct the request and retry", detail)
}
