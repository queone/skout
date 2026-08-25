package app

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/queone/skout/internal/transport"
)

type appExecutor struct {
	calls    int
	request  transport.ValidatedRequest
	response transport.Response
	err      error
}

func (executor *appExecutor) Execute(request transport.ValidatedRequest) (transport.Response, error) {
	executor.calls++
	executor.request = request
	return executor.response, executor.err
}

func TestFetchPinsAliasesHeadersBoundsAndDeterministicOutput(t *testing.T) {
	tests := []struct {
		host    string
		origin  string
		headers []transport.Header
	}{
		{host: "mlb", origin: "https://statsapi.mlb.com"},
		{host: "espn", origin: "https://site.api.espn.com", headers: []transport.Header{{Name: "User-Agent", Value: "skout/0.2.0 (+https://github.com/queone/skout)"}, {Name: "Accept", Value: "application/json"}}},
		{host: "oddsshark", origin: "https://www.oddsshark.com", headers: []transport.Header{{Name: "Referer", Value: "https://www.oddsshark.com/mlb/scores"}}},
		{host: "rotowire", origin: "https://www.rotowire.com"},
		{host: "savant", origin: "https://baseballsavant.mlb.com"},
		{host: "yahoo", origin: "https://pub-api-ro.fantasysports.yahoo.com", headers: []transport.Header{{Name: "Accept", Value: "application/json"}}},
		{host: "fangraphs", origin: "https://www.fangraphs.com", headers: []transport.Header{{Name: "User-Agent", Value: "Mozilla/5.0 (compatible; skout) AppleWebKit/537.36"}, {Name: "Accept", Value: "text/html,application/xhtml+xml"}}},
		{host: "fantasypros", origin: "https://www.fantasypros.com"},
	}
	for _, test := range tests {
		executor := &appExecutor{response: transport.Response{Status: 201, Headers: []transport.Header{{Name: "a", Value: "one"}, {Name: "x", Value: "two"}}, Body: []byte("payload")}}
		output, err := Fetch(transport.New(executor), "0.2.0", test.host, "/path?q=one")
		if err != nil {
			t.Fatalf("%s: %v", test.host, err)
		}
		if output != "HTTP 201\na: one\nx: two\n\npayload" {
			t.Errorf("%s output=%q", test.host, output)
		}
		if executor.calls != 1 || executor.request.URL() != test.origin+"/path?q=one" || executor.request.Timeout() != 20*time.Second || executor.request.BodyLimit() != 16*1024*1024 || !reflect.DeepEqual(executor.request.Headers(), test.headers) {
			t.Errorf("%s request=%s headers=%#v bounds=%v/%d", test.host, executor.request.URL(), executor.request.Headers(), executor.request.Timeout(), executor.request.BodyLimit())
		}
	}
}

func TestFetchPreservesValidTextAndReplacesInvalidUTF8LikeFrozenRenderer(t *testing.T) {
	executor := &appExecutor{response: transport.Response{Status: 200, Body: []byte{'a', 0, 'b', 0xff, 0xff, 'c', 0xe2, 0x82}}}
	output, err := Fetch(transport.New(executor), "0.2.0", "mlb", "/path")
	if err != nil {
		t.Fatal(err)
	}
	if output != "HTTP 200\n\na\x00b��c�" {
		t.Fatalf("output bytes=%q", []byte(output))
	}
}

func TestFetchRejectsUnknownAndOriginEscapingPathsBeforeDispatch(t *testing.T) {
	executor := &appExecutor{}
	for _, test := range []struct {
		host, path, want string
	}{{"unknown", "/", "fetch: unknown host unknown; choose a documented provider and retry"}, {"mlb", "https://evil.test/x", "fetch: path must remain on the selected provider origin; correct the path and retry"}, {"mlb", "//evil.test/x", "fetch: path must remain on the selected provider origin; correct the path and retry"}, {"unknown", "https://evil.test/x", "fetch: path must remain on the selected provider origin; correct the path and retry"}} {
		if _, err := Fetch(transport.New(executor), "0.2.0", test.host, test.path); err == nil || err.Error() != test.want {
			t.Errorf("Fetch(%q,%q) succeeded", test.host, test.path)
		}
	}
	if executor.calls != 0 {
		t.Fatalf("unsafe request dispatched %d times", executor.calls)
	}
	want := errors.New("network")
	executor.err = want
	if _, err := Fetch(transport.New(executor), "0.2.0", "mlb", "/x"); !errors.Is(err, want) || !strings.HasPrefix(err.Error(), "fetch:") {
		t.Fatalf("context error=%v", err)
	}
}
