package main

import (
	"strings"
	"testing"
)

const goodCmdline = "console=ttyS0 ro root=/dev/vda init=/sbin/arcana-init arcana.vsock_port=1024 arcana.pids=256 arcana.nofile=1024 arcana.workspace=/dev/vdb"

func TestParseCmdline_AcceptsTheDriversArguments(t *testing.T) {
	s, err := parseCmdline(goodCmdline)
	if err != nil || s.VsockPort != 1024 || s.PIDs != 256 || s.NoFile != 1024 || s.Workspace != "/dev/vdb" {
		t.Fatalf("%+v %v", s, err)
	}
	s, err = parseCmdline(strings.Replace(goodCmdline, "/dev/vdb", "tmpfs:2048m", 1))
	if err != nil || s.Workspace != "tmpfs:2048m" {
		t.Fatalf("%+v %v", s, err)
	}
}

func TestParseCmdline_RejectsMissingDuplicateAndMalformedValues(t *testing.T) {
	for name, line := range map[string]string{
		"missing port":     strings.Replace(goodCmdline, "arcana.vsock_port=1024", "", 1),
		"zero port":        strings.Replace(goodCmdline, "1024", "0", 1),
		"non numeric pids": strings.Replace(goodCmdline, "arcana.pids=256", "arcana.pids=lots", 1),
		"zero pids":        strings.Replace(goodCmdline, "arcana.pids=256", "arcana.pids=0", 1),
		"missing nofile":   strings.Replace(goodCmdline, "arcana.nofile=1024", "", 1),
		"duplicate":        goodCmdline + " arcana.pids=999999",
		"device traversal": strings.Replace(goodCmdline, "/dev/vdb", "/dev/../etc/shadow", 1),
		"other device":     strings.Replace(goodCmdline, "/dev/vdb", "/dev/sda", 1),
		"bad tmpfs size":   strings.Replace(goodCmdline, "/dev/vdb", "tmpfs:big", 1),
		"no workspace":     strings.Replace(goodCmdline, "arcana.workspace=/dev/vdb", "", 1),
	} {
		if _, err := parseCmdline(line); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParseEnvFile_RejectsMalformedLines(t *testing.T) {
	env, err := parseEnvFile("PATH=/usr/bin\nGOPATH=/workspace/.go\n\nEMPTY=\n")
	if err != nil || len(env) != 3 || env[1] != "GOPATH=/workspace/.go" {
		t.Fatalf("%v %v", env, err)
	}
	for _, bad := range []string{"NOEQUALS", "=value", "BAD NAME=1", "A=1\x00"} {
		if _, err = parseEnvFile(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err = parseEnvFile(strings.Repeat("A=1\n", 257)); err == nil {
		t.Error("an unbounded environment was accepted")
	}
}

func TestMountPlan_KeepsTheRootReadOnlyAndScratchMountsNonSetuid(t *testing.T) {
	s, _ := parseCmdline(goodCmdline)
	plan := mountPlan(s)
	targets := make([]string, 0, len(plan))
	for _, m := range plan {
		targets = append(targets, m.Target)
		if m.Flags&msNoSuid == 0 {
			t.Errorf("%s allows setuid binaries", m.Target)
		}
		if m.Target == "/workspace" && m.Source != "/dev/vdb" {
			t.Errorf("workspace source %s", m.Source)
		}
		if m.Target == "/run" && m.Flags&msNoExec == 0 {
			t.Error("/run must not allow execution")
		}
	}
	if got := strings.Join(targets, " "); got != "/proc /sys /dev /dev/pts /tmp /run /workspace" {
		t.Fatalf("mount order %s", got)
	}
	s.Workspace = "tmpfs:512m"
	last := mountPlan(s)[len(mountPlan(s))-1]
	if last.Type != "tmpfs" || !strings.Contains(last.Data, "size=512m") || !strings.Contains(last.Data, "uid=1000") {
		t.Fatalf("%+v", last)
	}
}
