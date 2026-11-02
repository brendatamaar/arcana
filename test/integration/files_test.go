//go:build linux && integration

package integration

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/orchestrator"
)

func assertFiles(t *testing.T, ctx context.Context, s *orchestrator.Service, p auth.Principal, id string, handler http.Handler, key string) {
	t.Helper()
	data := []byte{0, 255, 1, 128, 'x', '\n'}
	input := func(data []byte) io.ReadCloser { return io.NopCloser(bytes.NewReader(data)) }
	name := "/workspace/files/nested/data ; literal"
	if err := s.WriteFile(ctx, p, id, name, input(data)); err != nil {
		t.Fatal("file write", err)
	}
	var output bytes.Buffer
	if err := s.ReadFile(ctx, p, id, name, &output, nil); err != nil || !bytes.Equal(output.Bytes(), data) {
		t.Fatal("binary read", err)
	}
	items, err := s.ListFiles(ctx, p, id, "/workspace/files", 1)
	if err != nil || len(items) != 1 || items[0].Type != "directory" {
		t.Fatal("list depth 1", items, err)
	}
	items, err = s.ListFiles(ctx, p, id, "/workspace/files", 2)
	if err != nil || len(items) != 2 {
		t.Fatal("list depth 2", items, err)
	}
	if err = s.WriteFile(ctx, p, id, name, input(bytes.Repeat([]byte("x"), 1025))); !errors.Is(err, orchestrator.ErrFileTooLarge) {
		t.Fatal("upload cap", err)
	}
	output.Reset()
	if err = s.ReadFile(ctx, p, id, name, &output, nil); err != nil || !bytes.Equal(output.Bytes(), data) {
		t.Fatal("oversize write replaced original", err)
	}
	args := []string{"/bin/ln", "-s", "/etc/passwd", "/workspace/passwd-link"}
	if result, e := s.Exec(ctx, p, id, orchestrator.ExecRequest{Cmd: &args}); e != nil || result.ExitCode != 0 {
		t.Fatal("symlink setup", e)
	}
	args = []string{"/bin/cat", "/etc/passwd"}
	passwd, err := s.Exec(ctx, p, id, orchestrator.ExecRequest{Cmd: &args})
	if err != nil || passwd.ExitCode != 0 || passwd.Stdout == "" {
		t.Fatal("sandbox passwd", err)
	}
	output.Reset()
	if err = s.ReadFile(ctx, p, id, "/workspace/passwd-link", &output, nil); err != nil || output.String() != passwd.Stdout {
		t.Fatal("symlink did not resolve sandbox file", err)
	}
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	if err = tw.WriteHeader(&tar.Header{Name: "inside/data", Mode: 0o644, Size: int64(len(data))}); err != nil {
		t.Fatal(err)
	}
	if _, err = tw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err = tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err = gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err = s.UploadArchive(ctx, p, id, "/workspace/upload", input(archive.Bytes())); err != nil {
		t.Fatal("archive upload", err)
	}
	output.Reset()
	if err = s.DownloadArchive(ctx, p, id, "/workspace/upload", &output); err != nil || output.Len() == 0 {
		t.Fatal("archive download", err)
	}
	// Send the opaque sandbox archive back into a different sandbox directory.
	// Only the sandbox extracts it; the host never opens returned archive entries.
	if err = s.UploadArchive(ctx, p, id, "/workspace/roundtrip", input(output.Bytes())); err != nil {
		t.Fatal("archive roundtrip", err)
	}
	output.Reset()
	if err = s.ReadFile(ctx, p, id, "/workspace/roundtrip/inside/data", &output, nil); err != nil || !bytes.Equal(output.Bytes(), data) {
		t.Fatal("archive contents", err)
	}
	if err = s.DeleteFile(ctx, p, id, "/workspace/files", true); err != nil {
		t.Fatal("recursive delete", err)
	}
	if err = s.ReadFile(ctx, p, id, name, io.Discard, nil); !errors.Is(err, orchestrator.ErrFileNotFound) {
		t.Fatal("deleted path", err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	for _, path := range []string{"/workspace/../etc/passwd", "/workspace/a\x00b", "/etc/passwd"} {
		req, e := http.NewRequestWithContext(ctx, "GET", server.URL+"/v1/sandboxes/"+id+"/files?path="+url.QueryEscape(path), nil)
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("Authorization", "Bearer "+key)
		resp, e := server.Client().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Fatal("encoded traversal", resp.StatusCode)
		}
	}
	// Unknown-length HTTP upload must detect the probe byte and keep the old file.
	req, err := http.NewRequestWithContext(ctx, "PUT", server.URL+"/v1/sandboxes/"+id+"/files?path=/workspace/oversize", io.NopCloser(bytes.NewReader(bytes.Repeat([]byte("x"), 1025))))
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = -1
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 413 {
		t.Fatal("chunked upload limit", resp.StatusCode)
	}
}
