package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/brendatamaar/arcana/internal/helper/protocol"
)

func TestID_RejectsUnsafePaths(t *testing.T) {
	for _, value := range []string{"", "../bad", "/tmp/bad", "x.y", "x\x00", "A"} {
		if validID(value) {
			t.Fatalf("accepted %q", value)
		}
	}
	if !validID("exe_abc123") {
		t.Fatal("rejected valid ID")
	}
}

func TestRun_RejectsMissingCommandAndInvalidTimeout(t *testing.T) {
	for _, args := range [][]string{{}, {"run", "--id", "exe_test"}, {"run", "--id", "exe_test", "--timeout", "0", "--", "true"}, {"kill", "--id", "exe_test", "--grace", "11s"}, {"invalid", "--id", "exe_test"}} {
		if _, err := execute(args, t.TempDir()); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
}

func TestProcessIdentity_HandlesNamesAndRejectsMalformedData(t *testing.T) {
	suffix := make([]string, 20)
	for i := range suffix {
		suffix[i] = "0"
	}
	suffix[0], suffix[1], suffix[19] = "S", "2", "12345"
	info, err := parseProcess("42 (name ) with spaces) " + strings.Join(suffix, " "))
	if err != nil || info.PID != 42 || info.Parent != 2 || info.StartTicks != 12345 {
		t.Fatalf("identity: %+v %v", info, err)
	}
	for _, value := range []string{"", "1 bad", "0 (bad) S 1", "4 (bad) S x"} {
		if _, err := parseProcess(value); err == nil {
			t.Fatalf("accepted malformed identity: %q", value)
		}
	}
	processes := map[int]processInfo{2: {PID: 2, Parent: 1}, 3: {PID: 3, Parent: 2}, 4: {PID: 4, Parent: 3}, 5: {PID: 5, Parent: 1}}
	if descendantDepth(4, 2, processes) != 2 || descendantDepth(5, 2, processes) != 0 {
		t.Fatal("process ancestry classification failed")
	}
}

func TestMetadata_IsAtomicBoundedAndRootConfined(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	value := protocol.Status{ID: "exe_test", ExitCode: 7, CleanupComplete: true}
	if err = atomicMetadata(root, "exe_test.json", value); err != nil {
		t.Fatal(err)
	}
	data, err := readMetadata(root, "exe_test.json")
	if err != nil {
		t.Fatal(err)
	}
	var got protocol.Status
	if err = json.Unmarshal(data, &got); err != nil || got != value {
		t.Fatalf("metadata: %+v %v", got, err)
	}
	if err = root.WriteFile("large", make([]byte, metadataLimit+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = readMetadata(root, "large"); err == nil {
		t.Fatal("unbounded metadata read")
	}
	if _, err = readMetadata(root, "../outside"); err == nil {
		t.Fatal("metadata escaped root")
	}
}
