package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"sync"

	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/helper/protocol"
	"github.com/brendatamaar/arcana/internal/pathutil"
)

// File domain failures retain the standard API error vocabulary.
var (
	ErrFileNotFound = errors.New("sandbox file not found")
	ErrFileDenied   = errors.New("sandbox file access denied")
	ErrFileTooLarge = errors.New("sandbox file limit exceeded")
)

type (
	fileIO struct {
		input  io.ReadCloser
		output io.Writer
		limit  int64
	}
	countedInput struct {
		io.Reader
		closer  io.Closer
		mu      sync.Mutex
		count   int64
		readErr error
	}
)

func (r *countedInput) Read(data []byte) (int, error) {
	n, err := r.Reader.Read(data)
	r.mu.Lock()
	r.count += int64(n)
	if err != nil && err != io.EOF {
		r.readErr = err
	}
	r.mu.Unlock()
	return n, err
}
func (r *countedInput) Close() error { return r.closer.Close() }

func (s *Service) filePath(value string) (string, error) {
	clean, err := pathutil.Clean(value, s.cfg.Limits.Files.AllowedRoots)
	if err != nil {
		return "", ErrPath
	}
	return clean, nil
}

func (s *Service) fileArgs(op, filename string, limit int64) []string {
	args := []string{helperPath, op, "--path", filename, "--max-bytes", strconv.FormatInt(limit, 10)}
	for _, root := range s.cfg.Limits.Files.AllowedRoots {
		args = append(args, "--root", root)
	}
	return args
}

func (s *Service) fileExec(ctx context.Context, p auth.Principal, id string, argv []string, input io.ReadCloser, inputLimit int64, output io.Writer, outputLimit int64) (ExecResult, error) {
	data, _ := json.Marshal(argv)
	plan := execPlan{argv: argv, cwd: "/workspace", timeout: int64(s.cfg.Limits.Exec.DefaultTimeoutSeconds), command: string(data)}
	flow := &fileIO{output: output, limit: outputLimit}
	var tracked *countedInput
	if input != nil {
		tracked = &countedInput{Reader: io.LimitReader(input, inputLimit+1), closer: input}
		flow.input = tracked
	}
	result, err := s.executePlan(ctx, p, id, plan, nil, flow)
	if tracked != nil {
		tracked.mu.Lock()
		count, readErr := tracked.count, tracked.readErr
		tracked.mu.Unlock()
		if count > inputLimit {
			return result, ErrFileTooLarge
		}
		if err == nil && result.ExitCode == 0 && readErr != nil {
			err = readErr
		}
	}
	if err != nil {
		return result, err
	}
	if result.Truncated {
		return result, ErrFileTooLarge
	}
	if result.TimedOut {
		return result, ErrRuntime
	}
	switch result.ExitCode {
	case 0:
		return result, nil
	case protocol.FileNotFound:
		return result, ErrFileNotFound
	case protocol.FileDenied:
		return result, ErrFileDenied
	case protocol.FileTooLarge:
		return result, ErrFileTooLarge
	case protocol.FileNoSpace:
		return result, ErrQuota
	default:
		return result, ErrInvalid
	}
}

// WriteFile streams a bounded raw body to an atomic sandbox-local helper write.
func (s *Service) WriteFile(ctx context.Context, p auth.Principal, id, filename string, input io.ReadCloser) error {
	clean, err := s.filePath(filename)
	if err != nil {
		return err
	}
	var count *countedInput
	if input != nil {
		count = &countedInput{Reader: input, closer: input}
		input = count
	}
	_, err = s.fileExec(ctx, p, id, s.fileArgs("write", clean, s.cfg.Limits.Files.MaxFileBytes), input, s.cfg.Limits.Files.MaxFileBytes, io.Discard, 4096)
	if err == nil {
		var size int64
		if count != nil {
			count.mu.Lock()
			size = count.count
			count.mu.Unlock()
		}
		s.audit.Record("file.written", p.ID, id, map[string]any{"path": clean, "size_bytes": size})
	}
	return err
}

// ReadFile stats the opened sandbox descriptor, then streams only that byte count.
func (s *Service) ReadFile(ctx context.Context, p auth.Principal, id, filename string, output io.Writer, header func(int64)) error {
	if output == nil {
		return ErrInvalid
	}
	clean, err := s.filePath(filename)
	if err != nil {
		return err
	}
	receiver := &fileReceiver{output: output, header: header, limit: s.cfg.Limits.Files.MaxFileBytes}
	_, err = s.fileExec(ctx, p, id, s.fileArgs("read", clean, s.cfg.Limits.Files.MaxFileBytes), nil, 0, receiver, s.cfg.Limits.Files.MaxFileBytes+4096)
	if err != nil {
		return err
	}
	return receiver.finish()
}

// DeleteFile removes only sandbox paths; recursive removal does not follow symlinks.
func (s *Service) DeleteFile(ctx context.Context, p auth.Principal, id, filename string, recursive bool) error {
	clean, err := s.filePath(filename)
	if err != nil {
		return err
	}
	args := s.fileArgs("remove", clean, 1)
	if recursive {
		args = append(args, "--recursive")
	}
	if s.audit == nil {
		_, err = s.fileExec(ctx, p, id, args, nil, 0, io.Discard, 4096)
		return err
	}
	args = append(args, "--metadata")
	var metadata bytes.Buffer
	_, err = s.fileExec(ctx, p, id, args, nil, 0, &metadata, 4096)
	if err == nil {
		var header protocol.FileHeader
		if json.Unmarshal(metadata.Bytes(), &header) != nil {
			return ErrRuntime
		}
		s.audit.Record("file.deleted", p.ID, id, map[string]any{"path": clean, "size_bytes": header.SizeBytes, "recursive": recursive})
	}
	return err
}

// ListFiles returns bounded metadata from the helper's incremental directory traversal.
func (s *Service) ListFiles(ctx context.Context, p auth.Principal, id, filename string, depth int64) ([]protocol.FileEntry, error) {
	clean, err := s.filePath(filename)
	if err != nil {
		return nil, err
	}
	if depth < 1 || depth > 100 {
		return nil, ErrInvalid
	}
	args := append(s.fileArgs("ls", clean, 1), "--depth", strconv.FormatInt(depth, 10))
	var output bytes.Buffer
	if _, err = s.fileExec(ctx, p, id, args, nil, 0, &output, protocol.MaxListBytes); err != nil {
		return nil, err
	}
	var result struct {
		Items []protocol.FileEntry `json:"items"`
	}
	decoder := json.NewDecoder(&output)
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&result); err != nil || len(result.Items) > protocol.MaxListEntries {
		return nil, ErrRuntime
	}
	var extra any
	if err = decoder.Decode(&extra); err != io.EOF {
		return nil, ErrRuntime
	}
	for _, entry := range result.Items {
		if _, err = s.filePath(entry.Path); err != nil || entry.SizeBytes < 0 || len(entry.Name) > 4096 {
			return nil, ErrRuntime
		}
		switch entry.Type {
		case "file", "directory", "symlink", "other":
		default:
			return nil, ErrRuntime
		}
	}
	if result.Items == nil {
		result.Items = []protocol.FileEntry{}
	}
	return result.Items, nil
}

// UploadArchive extracts inside the sandbox; the host handles only compressed bytes.
func (s *Service) UploadArchive(ctx context.Context, p auth.Principal, id, filename string, input io.ReadCloser) error {
	clean, err := s.filePath(filename)
	if err != nil {
		return err
	}
	if _, err = s.fileExec(ctx, p, id, s.fileArgs("mkdir", clean, 1), nil, 0, io.Discard, 4096); err != nil {
		return err
	}
	args := []string{"/bin/tar", "-xzf", "-", "--no-same-owner", "--no-same-permissions", "-C", clean}
	var count *countedInput
	if input != nil {
		count = &countedInput{Reader: input, closer: input}
		input = count
	}
	_, err = s.fileExec(ctx, p, id, args, input, s.cfg.Limits.Files.MaxArchiveBytes, io.Discard, 4096)
	if err == nil {
		var size int64
		if count != nil {
			count.mu.Lock()
			size = count.count
			count.mu.Unlock()
		}
		s.audit.Record("archive.uploaded", p.ID, id, map[string]any{"path": clean, "size_bytes": size})
	}
	return err
}

// DownloadArchive creates the archive inside the sandbox and caps its opaque stream.
func (s *Service) DownloadArchive(ctx context.Context, p auth.Principal, id, filename string, output io.Writer) error {
	if output == nil {
		return ErrInvalid
	}
	clean, err := s.filePath(filename)
	if err != nil {
		return err
	}
	if _, err = s.fileExec(ctx, p, id, append(s.fileArgs("stat", clean, 1), "--directory"), nil, 0, io.Discard, 4096); err != nil {
		return err
	}
	args := []string{"/bin/tar", "-czf", "-", "-C", clean, "."}
	_, err = s.fileExec(ctx, p, id, args, nil, 0, output, s.cfg.Limits.Files.MaxArchiveBytes)
	return err
}

type fileReceiver struct {
	output             io.Writer
	header             func(int64)
	limit              int64
	prefix             []byte
	expected, received int64
	ready              bool
	err                error
}

func (r *fileReceiver) Write(data []byte) (int, error) {
	total := len(data)
	if r.err != nil {
		return 0, r.err
	}
	if !r.ready {
		index := bytes.IndexByte(data, '\n')
		if index < 0 {
			if len(r.prefix)+len(data) > 4096 {
				r.err = ErrRuntime
				return 0, r.err
			}
			r.prefix = append(r.prefix, data...)
			return total, nil
		}
		if len(r.prefix)+index > 4096 {
			r.err = ErrRuntime
			return 0, r.err
		}
		r.prefix = append(r.prefix, data[:index]...)
		var header struct {
			SizeBytes *int64 `json:"size_bytes"`
		}
		decoder := json.NewDecoder(bytes.NewReader(r.prefix))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&header); err != nil || header.SizeBytes == nil || *header.SizeBytes < 0 {
			r.err = ErrRuntime
			return 0, r.err
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			r.err = ErrRuntime
			return 0, r.err
		}
		if *header.SizeBytes > r.limit {
			r.err = ErrFileTooLarge
			return 0, r.err
		}
		r.expected = *header.SizeBytes
		r.ready = true
		if r.header != nil {
			r.header(r.expected)
		}
		data = data[index+1:]
	}
	if int64(len(data)) > r.expected-r.received {
		r.err = ErrFileTooLarge
		return 0, r.err
	}
	n, err := r.output.Write(data)
	r.received += int64(n)
	if err != nil {
		r.err = err
		return 0, err
	}
	if n != len(data) {
		r.err = io.ErrShortWrite
		return 0, r.err
	}
	return total, nil
}

func (r *fileReceiver) finish() error {
	if r.err != nil {
		return r.err
	}
	if !r.ready || r.received != r.expected {
		return ErrRuntime
	}
	return nil
}
