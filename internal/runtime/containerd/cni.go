package containerd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// cniTimeout bounds one plugin invocation. Plugins touch the kernel and iptables; a hung one must not hang a
// create.
const cniTimeout = 30 * time.Second

// runCNI invokes a CNI plugin the way the specification defines: the plugin binary, the command and
// attachment in the environment, and the network configuration on stdin. Nothing here needs the CNI
// libraries, only the plugin binaries on the host.
func runCNI(ctx context.Context, binDir, command string, conf []byte, containerID, netnsPath, ifName string) error {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(conf, &head); err != nil || head.Type == "" || strings.ContainsAny(head.Type, "/\x00") {
		return errors.New("invalid CNI configuration")
	}
	ctx, cancel := context.WithTimeout(ctx, cniTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(binDir, head.Type)) // #nosec G204 -- the plugin name is a fixed type from our own configuration, with no path separators
	cmd.Env = []string{
		"CNI_COMMAND=" + command, "CNI_CONTAINERID=" + containerID, "CNI_NETNS=" + netnsPath, "CNI_IFNAME=" + ifName,
		"CNI_PATH=" + binDir, "CNI_ARGS=IgnoreUnknown=1", "PATH=/usr/sbin:/usr/bin:/sbin:/bin",
	}
	cmd.Stdin = bytes.NewReader(conf)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		// Plugins report failures as JSON on stdout.
		var failure struct {
			Msg, Details string
		}
		if json.Unmarshal(stdout.Bytes(), &failure) == nil && failure.Msg != "" {
			// DEL must succeed when the interface is already gone, and the bridge plugin does not always
			// manage that: gVisor tears down the interfaces of a stopped sandbox itself, so the plugin can find
			// the link, then lose it. Found on a real runsc (ADR 0010); only DEL is tolerated.
			if command == "DEL" && strings.Contains(failure.Msg, "no such device") {
				return nil
			}
			return fmt.Errorf("CNI %s %s %s: %s %s", head.Type, command, ifName, failure.Msg, failure.Details)
		}
		return fmt.Errorf("CNI %s %s %s: %w: %s", head.Type, command, ifName, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
