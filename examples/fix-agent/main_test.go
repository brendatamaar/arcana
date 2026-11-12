package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestParseFlags_DefaultsAndBounds(t *testing.T) {
	env := map[string]string{"ARCANA_URL": "http://arcana:7070", "ARCANA_API_KEY": "k"}
	getenv := func(key string) string { return env[key] }
	f, err := parseFlags("eval", nil, getenv, &bytes.Buffer{})
	if err != nil || f.server != "http://arcana:7070" || f.apiKey != "k" || f.parallel != 1 || f.maxSteps != 20 || f.maxTokens != 60000 {
		t.Fatalf("%+v %v", f, err)
	}
	for _, args := range [][]string{{"--parallel", "0"}, {"--parallel", "11"}, {"--max-steps", "0"}, {"--max-steps", "101"}, {"--max-tokens", "10"}, {"--nope"}} {
		if _, err = parseFlags("eval", args, getenv, &bytes.Buffer{}); err == nil {
			t.Fatalf("%v accepted", args)
		}
	}
}

func TestRun_HelpUnknownCommandAndConfigurationErrors(t *testing.T) {
	none := func(string) string { return "" }
	var out bytes.Buffer
	if err := run(context.Background(), nil, none, &out, &bytes.Buffer{}); err != nil || !strings.Contains(out.String(), "verify-fixtures") {
		t.Fatalf("%v %q", err, out.String())
	}
	if err := run(context.Background(), []string{"bogus"}, none, &out, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"run"}, none, &out, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "--fixture") {
		t.Fatal(err)
	}
	bad := func(key string) string {
		if key == "FIXAGENT_BASE_URL" {
			return "ftp://nope"
		}
		return ""
	}
	if err := run(context.Background(), []string{"eval"}, bad, &out, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "http") {
		t.Fatal(err)
	}
}

func TestSelectFixtures_FiltersAndRejectsUnknownNames(t *testing.T) {
	f := &commonFlags{fixtures: "../fixtures", only: "go-nil-map, py-off-by-one"}
	got, err := selectFixtures(f)
	if err != nil || len(got) != 2 || got[0].Name != "go-nil-map" {
		t.Fatalf("%v %v", got, err)
	}
	f.only = "nope"
	if _, err = selectFixtures(f); err == nil {
		t.Fatal("unknown fixture accepted")
	}
}

func TestPrintResults_TableAndJSONReportFailuresHonestly(t *testing.T) {
	results := []TaskResult{
		{Fixture: "a", Language: "python", Solved: true, Steps: 3, Tokens: 900, Duration: 4 * time.Second, Reason: "solved", Attempts: []Attempt{{}}},
		{Fixture: "b", Language: "go", Steps: 20, Tokens: 60000, Duration: 90 * time.Second, Reason: "step budget", Attempts: []Attempt{{}}},
	}
	var table bytes.Buffer
	if err := printResults(&table, results, "m1", false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"FIXTURE", "pass", "FAIL", "step budget", "model m1: 1 of 2 fixtures solved"} {
		if !strings.Contains(table.String(), want) {
			t.Errorf("missing %q in:\n%s", want, table.String())
		}
	}
	var asJSON bytes.Buffer
	if err := printResults(&asJSON, results, "m1", true); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(asJSON.Bytes(), &rows); err != nil || len(rows) != 2 || rows[1]["solved"] != false || rows[0]["model"] != "m1" {
		t.Fatalf("%v %v", rows, err)
	}
}
