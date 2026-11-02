package api

import (
	"context"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/brendatamaar/arcana/internal/api/gen"
	"github.com/brendatamaar/arcana/internal/apierr"
	"github.com/brendatamaar/arcana/internal/auth"
)

type fileResponse struct {
	writer     http.ResponseWriter
	ctx        context.Context
	controller *http.ResponseController
	started    bool
	media      string
	now        func() time.Time
}

func (w *fileResponse) header(size int64) {
	w.writer.Header().Set("Content-Type", w.media)
	if size >= 0 {
		w.writer.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	w.started = true
	w.writer.WriteHeader(http.StatusOK)
}

func (w *fileResponse) Write(data []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	_ = w.controller.SetWriteDeadline(w.now().Add(5 * time.Second))
	if !w.started {
		w.header(-1)
	}
	return w.writer.Write(data)
}

type fileBody struct {
	io.ReadCloser
	controller *http.ResponseController
	now        func() time.Time
}

func (r fileBody) Close() error {
	_ = r.controller.SetReadDeadline(r.now())
	return r.ReadCloser.Close()
}

func fileTransport(w http.ResponseWriter, r *http.Request, media string) (*fileResponse, func()) {
	controller := http.NewResponseController(w)
	response := &fileResponse{writer: w, ctx: r.Context(), controller: controller, media: media, now: time.Now}
	ended := make(chan struct{})
	stop := context.AfterFunc(r.Context(), func() {
		defer close(ended)
		_ = controller.SetReadDeadline(time.Now())
		_ = controller.SetWriteDeadline(time.Now())
	})
	return response, func() {
		if !stop() {
			<-ended
		}
		_ = controller.SetReadDeadline(time.Time{})
		_ = controller.SetWriteDeadline(time.Time{})
	}
}

func (s *Server) fileError(w http.ResponseWriter, r *http.Request, response *fileResponse, err error) {
	if response != nil && response.started {
		panic(http.ErrAbortHandler)
	}
	w.Header().Del("Content-Length")
	s.sandboxFailure(w, r, err)
}

func (s *Server) fileMedia(w http.ResponseWriter, r *http.Request, want string) bool {
	actual, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || actual != want {
		s.failure(w, r, apierr.NewInvalidRequest("invalid file Content-Type"))
		return false
	}
	return true
}

// WriteFile streams a capped raw body to the sandbox-local write helper.
func (s *Server) WriteFile(w http.ResponseWriter, r *http.Request, id string, params gen.WriteFileParams) {
	if s.lifecycle == nil {
		s.notImplemented(w, r)
		return
	}
	if !s.fileMedia(w, r, "application/octet-stream") {
		return
	}
	response, stop := fileTransport(w, r, "")
	defer stop()
	p, _ := auth.FromContext(r.Context())
	if err := s.lifecycle.WriteFile(r.Context(), p, id, params.Path, fileBody{r.Body, response.controller, response.now}); err != nil {
		s.fileError(w, r, nil, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ReadFile forwards only the stat-checked descriptor's opaque byte count.
func (s *Server) ReadFile(w http.ResponseWriter, r *http.Request, id string, params gen.ReadFileParams) {
	if s.lifecycle == nil {
		s.notImplemented(w, r)
		return
	}
	response, stop := fileTransport(w, r, "application/octet-stream")
	defer stop()
	p, _ := auth.FromContext(r.Context())
	if err := s.lifecycle.ReadFile(r.Context(), p, id, params.Path, response, response.header); err != nil {
		s.fileError(w, r, response, err)
	}
}

// DeleteFile removes an owned sandbox path with explicit recursive opt-in.
func (s *Server) DeleteFile(w http.ResponseWriter, r *http.Request, id string, params gen.DeleteFileParams) {
	if s.lifecycle == nil {
		s.notImplemented(w, r)
		return
	}
	p, _ := auth.FromContext(r.Context())
	if err := s.lifecycle.DeleteFile(r.Context(), p, id, params.Path, params.Recursive != nil && *params.Recursive); err != nil {
		s.sandboxFailure(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListFiles returns a bounded helper-produced directory listing.
func (s *Server) ListFiles(w http.ResponseWriter, r *http.Request, id string, params gen.ListFilesParams) {
	if s.lifecycle == nil {
		s.notImplemented(w, r)
		return
	}
	depth := int64(1)
	if params.Depth != nil {
		depth = *params.Depth
	}
	p, _ := auth.FromContext(r.Context())
	entries, err := s.lifecycle.ListFiles(r.Context(), p, id, params.Path, depth)
	if err != nil {
		s.sandboxFailure(w, r, err)
		return
	}
	result := gen.FileList{Items: make([]gen.FileEntry, 0, len(entries))}
	for _, entry := range entries {
		result.Items = append(result.Items, gen.FileEntry{Name: entry.Name, Path: entry.Path, Type: gen.FileEntryType(entry.Type), SizeBytes: entry.SizeBytes, Mode: &entry.Mode, ModifiedAt: &entry.ModifiedAt})
	}
	s.respond(w, r, http.StatusOK, result)
}

// UploadArchive streams compressed bytes to tar inside the sandbox namespace.
func (s *Server) UploadArchive(w http.ResponseWriter, r *http.Request, id string, params gen.UploadArchiveParams) {
	if s.lifecycle == nil {
		s.notImplemented(w, r)
		return
	}
	if !s.fileMedia(w, r, "application/gzip") {
		return
	}
	response, stop := fileTransport(w, r, "")
	defer stop()
	p, _ := auth.FromContext(r.Context())
	if err := s.lifecycle.UploadArchive(r.Context(), p, id, params.Path, fileBody{r.Body, response.controller, response.now}); err != nil {
		s.fileError(w, r, nil, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DownloadArchive forwards a capped gzip stream produced entirely inside the sandbox.
func (s *Server) DownloadArchive(w http.ResponseWriter, r *http.Request, id string, params gen.DownloadArchiveParams) {
	if s.lifecycle == nil {
		s.notImplemented(w, r)
		return
	}
	response, stop := fileTransport(w, r, "application/gzip")
	defer stop()
	p, _ := auth.FromContext(r.Context())
	if err := s.lifecycle.DownloadArchive(r.Context(), p, id, params.Path, response); err != nil {
		s.fileError(w, r, response, err)
	}
}
