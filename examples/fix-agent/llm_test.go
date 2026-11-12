package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIChat_RequestShapeToolCallsAndUsage(t *testing.T) {
	var seen map[string]any
	var auth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&seen)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"/workspace/a\"}"}}]}}],"usage":{"total_tokens":42}}`)
	}))
	defer server.Close()
	llm, err := NewOpenAIChat(server.URL+"/v1/", "m", "secret-key")
	if err != nil {
		t.Fatal(err)
	}
	reply, err := llm.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, toolSpecs(), 0.3)
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer secret-key" || seen["model"] != "m" || seen["temperature"] != 0.3 || seen["stream"] != false || len(seen["tools"].([]any)) != 4 {
		t.Fatalf("request: auth=%q %v", auth, seen)
	}
	if reply.TotalTokens != 42 || len(reply.Message.ToolCalls) != 1 || reply.Message.ToolCalls[0].Function.Name != "read_file" {
		t.Fatalf("%+v", reply)
	}
}

func TestOpenAIChat_NoKeyEstimatedTokensAndFailures(t *testing.T) {
	mode := "ok"
	var auth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		switch mode {
		case "ok":
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"done"}}]}`)
		case "status":
			w.WriteHeader(500)
			_, _ = io.WriteString(w, strings.Repeat("boom ", 200))
		case "junk":
			_, _ = io.WriteString(w, `{"choices":[]}`)
		case "huge":
			_, _ = io.WriteString(w, strings.Repeat("x", maxLLMResponse+10))
		case "redirect":
			http.Redirect(w, r, "http://127.0.0.1:1/steal", http.StatusFound)
		}
	}))
	defer server.Close()
	llm, err := NewOpenAIChat(server.URL, "m", "")
	if err != nil {
		t.Fatal(err)
	}
	reply, err := llm.Chat(context.Background(), nil, nil, 0)
	if err != nil || reply.Message.Content != "done" || reply.TotalTokens <= 0 || auth != "" {
		t.Fatalf("%+v %v auth=%q", reply, err, auth)
	}
	for _, m := range []string{"status", "junk", "huge", "redirect"} {
		mode = m
		if _, err = llm.Chat(context.Background(), nil, nil, 0); err == nil {
			t.Fatalf("%s accepted", m)
		}
	}
	mode = "status"
	_, err = llm.Chat(context.Background(), nil, nil, 0)
	if len(err.Error()) > 300 {
		t.Fatal("error body not truncated")
	}
}

func TestNewOpenAIChat_ValidatesEndpointAndModel(t *testing.T) {
	for _, tc := range [][2]string{{"ftp://x", "m"}, {"not a url", "m"}, {"http://x", ""}, {"", "m"}} {
		if _, err := NewOpenAIChat(tc[0], tc[1], ""); err == nil {
			t.Fatalf("%v accepted", tc)
		}
	}
}
