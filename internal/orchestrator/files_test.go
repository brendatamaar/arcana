package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/brendatamaar/arcana/internal/helper/protocol"
	"github.com/brendatamaar/arcana/internal/runtime"
	"github.com/brendatamaar/arcana/internal/runtime/fake"
)

func TestFiles_ReadOpaqueBytesAndRequireCompleteHeader(t *testing.T) {
	for _, tc := range []struct {
		output string
		want   error
	}{
		{"{\"size_bytes\":3}\n\x00\xffx", nil},
		{"{}\n", ErrRuntime},
		{"{\"size_bytes\":3}\nx", ErrRuntime},
		{"{\"size_bytes\":1}\nxx", ErrFileTooLarge},
		{"{\"size_bytes\":-1}\n", ErrRuntime},
		{"{\"size_bytes\":100}\n", ErrFileTooLarge},
		{"{\"size_bytes\":0,\"extra\":1}\n", ErrRuntime},
	} {
		t.Run(tc.output, func(t *testing.T) {
			s, d, p, _, row, _ := execFixture(t)
			s.cfg.Limits.Files.MaxFileBytes = 10
			d.script = fake.Script{Stdout: []byte(tc.output)}
			var output bytes.Buffer
			var size int64 = -1
			err := s.ReadFile(context.Background(), p, row.ID, "/workspace/data", &output, func(n int64) { size = n })
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
			if err == nil && (size != 3 || output.String() != "\x00\xffx") {
				t.Fatal("binary transfer changed")
			}
		})
	}
}

func TestFiles_ValidationOwnershipAndDomainErrors(t *testing.T) {
	s, d, p, other, row, _ := execFixture(t)
	ctx := context.Background()
	for _, name := range []string{"relative", "/etc/passwd", "/workspace/../etc/passwd", "/workspace-other/a", "/tmp/\x00x"} {
		if err := s.DeleteFile(ctx, p, row.ID, name, false); !errors.Is(err, ErrPath) {
			t.Fatal(name, err)
		}
	}
	if err := s.WriteFile(ctx, other, row.ID, "/workspace/a", io.NopCloser(strings.NewReader("x"))); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if len(d.started) != 0 {
		t.Fatal("invalid/foreign request reached runtime")
	}
	for _, tc := range []struct {
		code int
		want error
	}{{protocol.FileNotFound, ErrFileNotFound}, {protocol.FileDenied, ErrFileDenied}, {protocol.FileTooLarge, ErrFileTooLarge}, {protocol.FileNoSpace, ErrQuota}, {protocol.FileInvalid, ErrInvalid}} {
		d.code = tc.code
		d.script = fake.Script{Exit: runtime.ExitInfo{ExitCode: protocol.TransportExit(tc.code)}}
		if err := s.DeleteFile(ctx, p, row.ID, "/workspace/a ; data", true); !errors.Is(err, tc.want) {
			t.Fatal(tc.code, err)
		}
		run := <-d.started
		if run.Cmd[len(run.Cmd)-1] != "--recursive" {
			t.Fatal("missing explicit recursive flag")
		}
	}
}

func TestFiles_ListBoundedMetadataAndArchiveArgv(t *testing.T) {
	s, d, p, _, row, _ := execFixture(t)
	ctx := context.Background()
	d.script = fake.Script{Stdout: []byte(`{"items":[{"name":"a","path":"/workspace/a","type":"file","size_bytes":3,"mode":420,"modified_at":"2026-11-01T00:00:00Z"}]}`)}
	items, err := s.ListFiles(ctx, p, row.ID, "/workspace", 100)
	if err != nil || len(items) != 1 || items[0].Name != "a" {
		t.Fatal(items, err)
	}
	<-d.started
	if _, err = s.ListFiles(ctx, p, row.ID, "/workspace", 101); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	d.script = fake.Script{Stdout: []byte(`{"items":[{"name":"a","path":"/etc/passwd","type":"file"}]}`)}
	if _, err = s.ListFiles(ctx, p, row.ID, "/workspace", 1); !errors.Is(err, ErrRuntime) {
		t.Fatal(err)
	}
	<-d.started
	d.script = fake.Script{Stdout: []byte("opaque")}
	var output bytes.Buffer
	if err = s.DownloadArchive(ctx, p, row.ID, "/workspace/a ; data", &output); err != nil || output.String() != "opaque" {
		t.Fatal(err)
	}
	<-d.started // directory stat
	run := <-d.started
	if strings.Join(run.Cmd[len(run.Cmd)-6:], "|") != "/bin/tar|-czf|-|-C|/workspace/a ; data|." {
		t.Fatal(run.Cmd)
	}
	if err = s.UploadArchive(ctx, p, row.ID, "/workspace/archive", io.NopCloser(strings.NewReader("opaque"))); err != nil {
		t.Fatal(err)
	}
}

func TestFileReceiver_FragmentedHeaderAndSinkFailure(t *testing.T) {
	var output bytes.Buffer
	r := fileReceiver{output: &output, limit: 3}
	for _, chunk := range []string{"{\"size_", "bytes\":3}", "\n", "a", "bc"} {
		if _, err := r.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if r.finish() != nil || output.String() != "abc" {
		t.Fatal("fragmentation lost bytes")
	}
	r = fileReceiver{output: io.Discard, limit: 3}
	if _, err := r.Write(bytes.Repeat([]byte("x"), 4097)); !errors.Is(err, ErrRuntime) {
		t.Fatal(err)
	}
}
