package transport

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

type recordingExecutor struct {
	calls    int
	request  ValidatedRequest
	response Response
	err      error
}

func (executor *recordingExecutor) Execute(request ValidatedRequest) (Response, error) {
	executor.calls++
	executor.request = request
	return executor.response, executor.err
}

func TestValidationPrecedesInjectedDispatchAndProtectsSecrets(t *testing.T) {
	executor := &recordingExecutor{}
	client := New(executor)
	bad := []Request{
		{Method: Get, URL: "http://example.com", Timeout: time.Second, BodyLimit: 1},
		{Method: Get, URL: "https://user:secret@example.com", Timeout: time.Second, BodyLimit: 1},
		{Method: Get, URL: "https://example.com", Body: []byte("body"), Timeout: time.Second, BodyLimit: 1},
		{Method: Get, URL: "https://example.com", Headers: []Header{{Name: "X-Test", Value: "bad\nvalue"}}, Timeout: time.Second, BodyLimit: 1},
	}
	for _, request := range bad {
		if _, err := client.Execute(request); err == nil {
			t.Errorf("request %#v succeeded", request)
		}
	}
	if executor.calls != 0 {
		t.Fatalf("invalid requests dispatched %d times", executor.calls)
	}
	executor.response = Response{Status: 200}
	if _, err := client.Execute(Request{Method: Get, URL: "https://example.com", Timeout: time.Second, BodyLimit: 10}); err != nil {
		t.Fatal(err)
	}
	if executor.calls != 1 || executor.request.Timeout() != time.Second || executor.request.BodyLimit() != 10 {
		t.Fatalf("validated request=%#v calls=%d", executor.request, executor.calls)
	}
	if got := (Header{Name: "Authorization", Value: "secret"}).String(); strings.Contains(got, "secret") {
		t.Fatalf("header String leaked: %s", got)
	}
}

func TestProductionExecutorBoundsRedirectsHeadersAndUserAgent(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		if request.Header.Get("User-Agent") != "" {
			t.Errorf("implicit user agent=%q", request.Header.Get("User-Agent"))
		}
		if _, exists := request.Header["Accept-Encoding"]; exists {
			t.Errorf("implicit Accept-Encoding=%q", request.Header.Values("Accept-Encoding"))
		}
		if calls <= 2 {
			http.Redirect(writer, request, "/next", http.StatusFound)
			return
		}
		writer.Header().Add("X-Multi", "one")
		writer.Header().Add("X-Multi", "two")
		writer.Header().Set("A-First", "yes")
		_, _ = writer.Write([]byte("body"))
	}))
	defer server.Close()
	response, err := Production().Execute(Request{Method: Get, URL: server.URL, Timeout: time.Second, BodyLimit: 4})
	if err == nil {
		t.Fatal("redirect loop should fail before body")
	}
	if !strings.Contains(err.Error(), "loop") {
		t.Fatalf("redirect error=%v", err)
	}

	calls = 0
	server.Config.Handler = http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Add("X-Multi", "one")
		writer.Header().Add("X-Multi", "two")
		writer.Header().Set("A-First", "yes")
		_, _ = writer.Write([]byte("body"))
	})
	response, err = Production().Execute(Request{Method: Get, URL: server.URL, Timeout: time.Second, BodyLimit: 4})
	if err != nil {
		t.Fatal(err)
	}
	if response.Status != 200 || string(response.Body) != "body" || response.Headers[0].Name != "a-first" || !containsHeaderPair(response.Headers, "x-multi", "one") || !containsHeaderPair(response.Headers, "x-multi", "two") {
		t.Fatalf("response=%#v", response)
	}
	if values := headerValues(response.Headers, "x-multi"); !reflect.DeepEqual(values, []string{"one", "two"}) {
		t.Fatalf("duplicate values=%#v", values)
	}
	_, err = Production().Execute(Request{Method: Get, URL: server.URL, Timeout: time.Second, BodyLimit: 3})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("body limit error=%v", err)
	}
}

func headerValues(headers []Header, name string) []string {
	var values []string
	for _, header := range headers {
		if header.Name == name {
			values = append(values, header.Value)
		}
	}
	return values
}

func containsHeaderPair(headers []Header, name, value string) bool {
	for _, header := range headers {
		if header.Name == name && header.Value == value {
			return true
		}
	}
	return false
}

func TestInjectedExecutionFailureIsReturned(t *testing.T) {
	want := errors.New("provider failed")
	_, err := New(&recordingExecutor{err: want}).Execute(Request{Method: Get, URL: "https://example.com", Timeout: time.Second, BodyLimit: 1})
	if !errors.Is(err, want) {
		t.Fatalf("error=%v", err)
	}
}

func TestRedirectLimitCrossOriginStrippingAndErrorRedaction(t *testing.T) {
	redirects := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		step, _ := strconv.Atoi(strings.TrimPrefix(request.URL.Path, "/"))
		limit, _ := strconv.Atoi(request.URL.Query().Get("limit"))
		if step < limit {
			http.Redirect(writer, request, fmt.Sprintf("/%d?limit=%d", step+1, limit), http.StatusFound)
			return
		}
		_, _ = writer.Write([]byte("ok"))
	}))
	defer redirects.Close()
	if response, err := Production().Execute(Request{Method: Get, URL: redirects.URL + "/0?limit=10", Timeout: time.Second, BodyLimit: 2}); err != nil || string(response.Body) != "ok" {
		t.Fatalf("ten redirects response=%#v err=%v", response, err)
	}
	if _, err := Production().Execute(Request{Method: Get, URL: redirects.URL + "/0?limit=11", Timeout: time.Second, BodyLimit: 2}); err == nil || !strings.Contains(err.Error(), "redirect limit") {
		t.Fatalf("eleven redirects error=%v", err)
	}
	var leaked string
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		leaked = request.Header.Get("Authorization") + request.Header.Get("Proxy-Authorization") + request.Header.Get("Cookie") + request.Header.Get("Cookie2")
		_, _ = writer.Write([]byte("ok"))
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusFound)
	}))
	defer source.Close()
	if _, err := Production().Execute(Request{Method: Get, URL: source.URL, Headers: []Header{{Name: "Authorization", Value: "Bearer secret"}, {Name: "Proxy-Authorization", Value: "Basic secret"}, {Name: "Cookie", Value: "session=secret"}, {Name: "Cookie2", Value: "legacy=secret"}}, Timeout: time.Second, BodyLimit: 2}); err != nil {
		t.Fatal(err)
	}
	if leaked != "" {
		t.Fatalf("cross-origin sensitive headers leaked: %q", leaked)
	}
	_, err := Production().Execute(Request{Method: Get, URL: "http://127.0.0.1:1/path?token=top-secret", Timeout: time.Millisecond, BodyLimit: 1})
	if err == nil || strings.Contains(err.Error(), "top-secret") {
		t.Fatalf("redacted error=%v", err)
	}
}

func TestProductionExecutorFollowsAllRedirectStatusesAndRejectsUnsafeTargets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/start" {
			writer.Header().Set("Location", "/end")
			writer.WriteHeader(300)
			return
		}
		_, _ = writer.Write([]byte("ok"))
	}))
	defer server.Close()
	response, err := Production().Execute(Request{Method: Get, URL: server.URL + "/start", Timeout: time.Second, BodyLimit: 2})
	if err != nil || string(response.Body) != "ok" {
		t.Fatalf("300 response=%#v err=%v", response, err)
	}
	unsafe := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Location", "http://example.com/next")
		writer.WriteHeader(http.StatusFound)
	}))
	defer unsafe.Close()
	if _, err := Production().Execute(Request{Method: Get, URL: unsafe.URL, Timeout: time.Second, BodyLimit: 2}); err == nil || !strings.Contains(err.Error(), "non-loopback") {
		t.Fatalf("unsafe redirect error=%v", err)
	}
	for _, test := range []struct{ previous, next string }{
		{"https://example.com/start", "http://example.com/end"},
		{"https://example.com/start", "https://user:secret@example.com/end"},
		{"https://example.com/start", "https://example.com/end#fragment"},
	} {
		previous, _ := url.Parse(test.previous)
		next, _ := url.Parse(test.next)
		if err := validateRedirectURL(previous, next); err == nil {
			t.Errorf("redirect %s -> %s accepted", test.previous, test.next)
		}
	}
}

func TestProductionExecutorEnforcesReadDeadlineAndStreamedLimit(t *testing.T) {
	started := make(chan struct{})
	deadlineServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		close(started)
		<-request.Context().Done()
	}))
	defer deadlineServer.Close()
	start := time.Now()
	_, err := Production().Execute(Request{Method: Get, URL: deadlineServer.URL, Timeout: 50 * time.Millisecond, BodyLimit: 8})
	<-started
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("body deadline error=%v elapsed=%v", err, time.Since(start))
	}
	streamed := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		_, _ = writer.Write([]byte("body"))
	}))
	defer streamed.Close()
	if _, err := Production().Execute(Request{Method: Get, URL: streamed.URL, Timeout: time.Second, BodyLimit: 3}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("streamed body limit error=%v", err)
	}
}

func TestProductionExecutionErrorsAndHeaderValuesDoNotExposeProviderBytes(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		_, _ = connection.Write([]byte("HTTP/1.1 200 OK\r\nmalformed SENTINEL\r\n\r\n"))
		_ = connection.Close()
	}()
	_, err = Production().Execute(Request{Method: Get, URL: "http://" + listener.Addr().String(), Timeout: time.Second, BodyLimit: 8})
	_ = listener.Close()
	<-done
	if err == nil || strings.Contains(err.Error(), "SENTINEL") {
		t.Fatalf("execution error=%v", err)
	}
	response, err := readResponse(&http.Response{StatusCode: 200, Header: http.Header{"X-Binary": []string{"valid", "bad\xffvalue"}}, Body: io.NopCloser(strings.NewReader(""))}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if values := headerValues(response.Headers, "x-binary"); !reflect.DeepEqual(values, []string{"valid", "<non-text>"}) {
		t.Fatalf("header values=%#v", values)
	}
}
