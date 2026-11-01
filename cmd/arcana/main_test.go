package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersion_PrintsBuildMetadata(t *testing.T) {
	root := newRootCommand()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetArgs([]string{"version"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	want := "arcana " + version + " (commit " + commit + ", built " + date + ")\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

func TestCommand_RejectsInvalidArguments(t *testing.T) {
	for _, args := range [][]string{{"version", "extra"}, {"unknown"}} {
		root := newRootCommand()
		root.SetArgs(args)
		if err := root.Execute(); err == nil {
			t.Fatalf("args %q: expected error", args)
		}
	}
}

func TestRoot_ShowsHelp(t *testing.T) {
	root := newRootCommand()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "version") {
		t.Fatalf("help missing version command: %s", &output)
	}
}

func TestRoot_ConfigFlag(t *testing.T) {
	root := newRootCommand()
	root.SetArgs([]string{"--config", "/custom/config.yaml", "version"})
	var out bytes.Buffer
	root.SetOut(&out)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	got, err := root.PersistentFlags().GetString("config")
	if err != nil || got != "/custom/config.yaml" {
		t.Fatalf("config = %q, error %v", got, err)
	}
}
