package containerd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakePlugin installs a CNI "plugin" that records its environment and standard input, then fails with a message.
func fakePlugin(t *testing.T, failure string) (binDir, record string) {
	t.Helper()
	binDir = t.TempDir()
	record = filepath.Join(binDir, "record")
	script := "#!/bin/sh\n{ echo \"$CNI_COMMAND|$CNI_CONTAINERID|$CNI_NETNS|$CNI_IFNAME|$CNI_PATH\"; cat; } > '" + record + "'\n"
	if failure != "" {
		script += "echo '{\"cniVersion\":\"1.0.0\",\"code\":999,\"msg\":\"" + failure + "\"}'\nexit 1\n"
	}
	if err := os.WriteFile(filepath.Join(binDir, "bridge"), []byte(script), 0o700); err != nil { // #nosec G306 -- an executable test fixture in a private temp directory
		t.Fatal(err)
	}
	return binDir, record
}

func TestRunCNI_FollowsTheCNIProtocol(t *testing.T) {
	bin, record := fakePlugin(t, "")
	conf := []byte(`{"cniVersion":"1.0.0","name":"n","type":"bridge"}`)
	if err := runCNI(context.Background(), bin, "ADD", conf, "ctr", "/run/netns/x", "eth1"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(record) // #nosec G304 -- a path this test created
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got), "ADD|ctr|/run/netns/x|eth1|"+bin+"\n") || !strings.Contains(string(got), `"type":"bridge"`) {
		t.Fatalf("plugin saw %q", got)
	}
}

func TestRunCNI_RefusesAPluginNameThatCouldEscapeTheDirectory(t *testing.T) {
	bin, _ := fakePlugin(t, "")
	for _, typ := range []string{"../bridge", "a/b", ""} {
		conf := []byte(`{"type":"` + typ + `"}`)
		if err := runCNI(context.Background(), bin, "ADD", conf, "c", "/n", "eth0"); err == nil {
			t.Errorf("type %q accepted", typ)
		}
	}
	if err := runCNI(context.Background(), bin, "ADD", []byte("not json"), "c", "/n", "eth0"); err == nil {
		t.Error("garbage configuration accepted")
	}
}

func TestRunCNI_OnlyDELToleratesAnInterfaceThatIsAlreadyGone(t *testing.T) {
	bin, _ := fakePlugin(t, "failed to delete \\\"eth0\\\": no such device")
	conf := []byte(`{"cniVersion":"1.0.0","name":"n","type":"bridge"}`)
	if err := runCNI(context.Background(), bin, "DEL", conf, "c", "/n", "eth0"); err != nil {
		t.Fatalf("DEL of a vanished interface must succeed: %v", err)
	}
	if err := runCNI(context.Background(), bin, "ADD", conf, "c", "/n", "eth0"); err == nil || !strings.Contains(err.Error(), "no such device") {
		t.Fatalf("ADD must report the failure: %v", err)
	}
	other, _ := fakePlugin(t, "permission denied")
	if err := runCNI(context.Background(), other, "DEL", conf, "c", "/n", "eth0"); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("other DEL failures must surface: %v", err)
	}
}
