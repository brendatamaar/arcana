package envd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/brendatamaar/arcana/internal/envd/envdpb"
	"github.com/brendatamaar/arcana/internal/helper/protocol"
	"github.com/brendatamaar/arcana/internal/pathutil"
)

// File operations run in child processes (this binary's file utilities) as the sandbox
// user, so a daemon running as root never opens a sandbox path itself and symlinks can
// only resolve inside the sandbox.

func (s *Server) filePath(value string) (string, error) {
	clean, err := pathutil.Clean(value, s.cfg.Roots)
	if err != nil {
		return "", status.Error(codes.InvalidArgument, "invalid path")
	}
	return clean, nil
}

func (s *Server) fileArgs(op, path string, extra ...string) []string {
	args := []string{op, "--path", path}
	for _, root := range s.cfg.Roots {
		args = append(args, "--root", root)
	}
	return append(args, extra...)
}

func (s *Server) maxBytes(requested uint64) (int64, error) {
	if requested == 0 {
		return s.cfg.MaxFileBytes, nil
	}
	if requested > uint64(s.cfg.MaxFileBytes) { // #nosec G115 -- MaxFileBytes is validated positive
		return 0, status.Error(codes.InvalidArgument, "max_bytes exceeds the daemon limit")
	}
	return int64(requested), nil // #nosec G115 -- requested <= MaxFileBytes, which is an int64
}

func (s *Server) acquireFileOp(ctx context.Context) (func(), error) {
	select {
	case s.fileOps <- struct{}{}:
		return func() { <-s.fileOps }, nil
	case <-ctx.Done():
		return nil, status.Error(codes.ResourceExhausted, "too many concurrent file operations")
	}
}

func fileError(exit int) error {
	if exit == 0 {
		return nil
	}
	return status.Error(FileCode(exit), "file operation failed")
}

// WriteFile receives a header then chunks and writes the file atomically.
func (s *Server) WriteFile(stream envdpb.Envd_WriteFileServer) error {
	first, err := stream.Recv()
	if err != nil || first.GetHeader() == nil {
		return status.Error(codes.InvalidArgument, "the first message must be a header")
	}
	path, err := s.filePath(first.GetHeader().GetPath())
	if err != nil {
		return err
	}
	limit, err := s.maxBytes(first.GetHeader().GetMaxBytes())
	if err != nil {
		return err
	}
	release, err := s.acquireFileOp(stream.Context())
	if err != nil {
		return err
	}
	defer release()
	ctx, cancel := context.WithTimeout(stream.Context(), s.cfg.FileTimeout)
	defer cancel()
	// #nosec G204 -- re-running this binary's file utility with validated arguments
	cmd := exec.CommandContext(ctx, s.cfg.Self, s.childArgs(s.fileArgs("write", path, "--max-bytes", strconv.FormatInt(limit, 10)))...)
	cmd.Dir, cmd.Env, cmd.SysProcAttr = "/", []string{}, s.sysProc()
	// Cancellation (client disconnect or timeout) lets the writer remove its temporary
	// file; only a writer that ignores SIGTERM for 5 seconds is killed outright.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return status.Error(codes.Internal, "could not start the file writer")
	}
	if err = cmd.Start(); err != nil {
		return status.Error(codes.Internal, "could not start the file writer")
	}
	var total int64
	var streamErr error
	aborted := false
	for streamErr == nil {
		message, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			streamErr, aborted = recvErr, true
			break
		}
		chunk := message.GetChunk()
		total += int64(len(chunk))
		if len(chunk) > MaxChunkBytes {
			streamErr, aborted = status.Error(codes.InvalidArgument, "chunk exceeds the maximum size"), true
			break
		}
		// An oversized chunk is still forwarded: the writer counts limit+1 bytes itself,
		// refuses the file and removes its temporary, so the old content survives.
		if _, werr := stdin.Write(chunk); werr != nil && streamErr == nil {
			streamErr, aborted = status.Error(codes.Internal, "file writer stopped early"), true
		}
		if total > limit && streamErr == nil {
			streamErr = status.Error(codes.ResourceExhausted, "file exceeds the size limit")
		}
	}
	if aborted {
		// An incomplete upload must never be committed, so ask the writer to clean up
		// instead of ending its input as if the file were whole.
		defer abortChild(cmd)()
	}
	_ = stdin.Close()
	waitErr := cmd.Wait()
	if streamErr != nil {
		return streamErr
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		return fileError(exitErr.ExitCode())
	}
	if waitErr != nil {
		return status.Error(codes.Internal, "file writer failed")
	}
	return stream.SendAndClose(&envdpb.WriteFileResponse{SizeBytes: uint64(total)})
}

// ReadFile sends the size of the opened regular file, then exactly that many bytes.
func (s *Server) ReadFile(request *envdpb.ReadFileRequest, stream envdpb.Envd_ReadFileServer) error {
	path, err := s.filePath(request.GetPath())
	if err != nil {
		return err
	}
	limit, err := s.maxBytes(request.GetMaxBytes())
	if err != nil {
		return err
	}
	release, err := s.acquireFileOp(stream.Context())
	if err != nil {
		return err
	}
	defer release()
	ctx, cancel := context.WithTimeout(stream.Context(), s.cfg.FileTimeout)
	defer cancel()
	// #nosec G204 -- re-running this binary's file utility with validated arguments
	cmd := exec.CommandContext(ctx, s.cfg.Self, s.childArgs(s.fileArgs("read", path, "--max-bytes", strconv.FormatInt(limit, 10)))...)
	cmd.Dir, cmd.Env, cmd.SysProcAttr = "/", []string{}, s.sysProc()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return status.Error(codes.Internal, "could not start the file reader")
	}
	if err = cmd.Start(); err != nil {
		return status.Error(codes.Internal, "could not start the file reader")
	}
	streamErr := s.relay(bufio.NewReaderSize(stdout, MaxChunkBytes), stream, limit)
	if streamErr != nil {
		cancel()
	}
	waitErr := cmd.Wait()
	if streamErr != nil && !errors.Is(streamErr, errNoHeader) {
		return streamErr
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		return fileError(exitErr.ExitCode())
	}
	if waitErr != nil || streamErr != nil {
		return status.Error(codes.Internal, "file reader failed")
	}
	return nil
}

var errNoHeader = errors.New("reader produced no header")

func (s *Server) relay(reader *bufio.Reader, stream envdpb.Envd_ReadFileServer, limit int64) error {
	line, err := reader.ReadSlice('\n')
	if err != nil {
		return errNoHeader
	}
	var header protocol.FileHeader
	if err = json.Unmarshal(line, &header); err != nil || header.SizeBytes < 0 || header.SizeBytes > limit {
		return status.Error(codes.Internal, "invalid file header")
	}
	if err = stream.Send(&envdpb.ReadFileResponse{Message: &envdpb.ReadFileResponse_SizeBytes{SizeBytes: uint64(header.SizeBytes)}}); err != nil {
		return err
	}
	buffer := make([]byte, MaxChunkBytes)
	for remaining := header.SizeBytes; remaining > 0; {
		n, readErr := reader.Read(buffer[:min(int64(len(buffer)), remaining)])
		if n > 0 {
			remaining -= int64(n)
			if err = stream.Send(&envdpb.ReadFileResponse{Message: &envdpb.ReadFileResponse_Chunk{Chunk: buffer[:n]}}); err != nil {
				return err
			}
		}
		if readErr != nil && remaining > 0 {
			return status.Error(codes.DataLoss, "file ended before its announced size")
		}
	}
	return nil
}

// ListFiles returns a bounded directory listing.
func (s *Server) ListFiles(ctx context.Context, request *envdpb.ListFilesRequest) (*envdpb.ListFilesResponse, error) {
	path, err := s.filePath(request.GetPath())
	if err != nil {
		return nil, err
	}
	depth := int(request.GetDepth())
	if depth == 0 {
		depth = 1
	}
	if depth < 1 || depth > 100 {
		return nil, status.Error(codes.InvalidArgument, "depth must be 1..100")
	}
	out, err := s.runFileOp(ctx, protocol.MaxListBytes, s.fileArgs("ls", path, "--depth", strconv.Itoa(depth)))
	if err != nil {
		return nil, err
	}
	var listing struct {
		Items []protocol.FileEntry `json:"items"`
	}
	if err = json.Unmarshal(out, &listing); err != nil || len(listing.Items) > protocol.MaxListEntries {
		return nil, status.Error(codes.Internal, "invalid directory listing")
	}
	response := &envdpb.ListFilesResponse{Items: make([]*envdpb.FileEntry, 0, len(listing.Items))}
	for _, item := range listing.Items {
		response.Items = append(response.Items, &envdpb.FileEntry{Name: item.Name, Path: item.Path, Type: item.Type, SizeBytes: item.SizeBytes, Mode: item.Mode, ModifiedUnixNanos: item.ModifiedAt.UnixNano()})
	}
	return response, nil
}

// RemoveFile deletes a path and reports the size observed before removal.
func (s *Server) RemoveFile(ctx context.Context, request *envdpb.RemoveFileRequest) (*envdpb.RemoveFileResponse, error) {
	path, err := s.filePath(request.GetPath())
	if err != nil {
		return nil, err
	}
	args := s.fileArgs("remove", path, "--metadata")
	if request.GetRecursive() {
		args = append(args, "--recursive")
	}
	out, err := s.runFileOp(ctx, 1024, args)
	if err != nil {
		return nil, err
	}
	var header protocol.FileHeader
	if err = json.Unmarshal(out, &header); err != nil {
		return nil, status.Error(codes.Internal, "invalid removal result")
	}
	return &envdpb.RemoveFileResponse{RemovedSizeBytes: header.SizeBytes}, nil
}

// MakeDirectory creates a directory and its parents.
func (s *Server) MakeDirectory(ctx context.Context, request *envdpb.MakeDirectoryRequest) (*envdpb.MakeDirectoryResponse, error) {
	path, err := s.filePath(request.GetPath())
	if err != nil {
		return nil, err
	}
	if _, err = s.runFileOp(ctx, 1024, s.fileArgs("mkdir", path)); err != nil {
		return nil, err
	}
	return &envdpb.MakeDirectoryResponse{}, nil
}

// runFileOp runs one bounded file utility and maps its exit code to a gRPC error.
func (s *Server) runFileOp(ctx context.Context, limit int64, args []string) ([]byte, error) {
	release, err := s.acquireFileOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, s.cfg.FileTimeout)
	defer cancel()
	out, exit, err := s.capture(ctx, nil, limit, args...)
	if err != nil {
		return nil, status.Error(codes.Internal, "file operation failed")
	}
	return out, fileError(exit)
}

// abortChild asks a file writer to remove its temporary file and stop. The returned
// function disarms the forced kill that follows if it does not stop within 5 seconds.
func abortChild(cmd *exec.Cmd) func() {
	_ = cmd.Process.Signal(syscall.SIGTERM)
	timer := time.AfterFunc(5*time.Second, func() { _ = cmd.Process.Kill() })
	return func() { timer.Stop() }
}
