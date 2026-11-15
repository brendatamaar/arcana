package firecracker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// apiClient speaks the small part of Firecracker's REST API the driver needs. It uses
// only the standard library: five PUT requests do not justify the SDK and its
// dependencies (ADR 0007).
type apiClient struct {
	http *http.Client
}

func newAPIClient(socket string) *apiClient {
	return &apiClient{http: &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			},
			DisableKeepAlives: true,
		},
	}}
}

// apiError carries Firecracker's fault message, which is safe to show operators.
type apiError struct {
	Path   string
	Status int
	Fault  string
}

func (e apiError) Error() string {
	return fmt.Sprintf("firecracker %s: HTTP %d: %s", e.Path, e.Status, e.Fault)
}

func (c *apiClient) put(ctx context.Context, path string, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, "http://firecracker"+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("firecracker %s: %w", path, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode/100 == 2 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return nil
	}
	var fault struct {
		Message string `json:"fault_message"`
	}
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	if json.Unmarshal(raw, &fault) != nil || fault.Message == "" {
		fault.Message = strings.TrimSpace(string(raw))
	}
	return apiError{Path: path, Status: response.StatusCode, Fault: fault.Message}
}

// configure applies the plan and boots the guest. The order follows Firecracker's
// requirement that every device is configured before InstanceStart.
func (c *apiClient) configure(ctx context.Context, plan vmPlan, l layout) error {
	rootfs := l.Rootfs
	if !l.Jailed {
		rootfs = plan.Rootfs
	}
	steps := []struct {
		path string
		body any
	}{
		{"/boot-source", map[string]any{"kernel_image_path": l.Kernel, "boot_args": plan.KernelArgs}},
		{"/machine-config", map[string]any{"vcpu_count": plan.VCPUs, "mem_size_mib": plan.MemoryMiB, "smt": false}},
		{"/drives/rootfs", map[string]any{"drive_id": "rootfs", "path_on_host": rootfs, "is_root_device": true, "is_read_only": true}},
	}
	if !plan.TmpfsSpace {
		steps = append(steps, struct {
			path string
			body any
		}{"/drives/workspace", map[string]any{"drive_id": "workspace", "path_on_host": l.Workspace, "is_root_device": false, "is_read_only": false}})
	}
	steps = append(steps,
		struct {
			path string
			body any
		}{"/vsock", map[string]any{"guest_cid": guestCID, "uds_path": l.Vsock}},
		struct {
			path string
			body any
		}{"/actions", map[string]any{"action_type": "InstanceStart"}},
	)
	for _, step := range steps {
		if err := c.put(ctx, step.path, step.body); err != nil {
			return err
		}
	}
	return nil
}

var errNoAPI = errors.New("firecracker API socket did not appear")
