package api

import (
	"net/http"

	"github.com/brendatamaar/arcana/internal/api/gen"
)

// ListSandboxes is implemented by its later Phase 1 task.
func (s *Server) ListSandboxes(w http.ResponseWriter, r *http.Request, _ gen.ListSandboxesParams) {
	s.notImplemented(w, r)
}

// CreateSandbox is implemented by its later Phase 1 task.
func (s *Server) CreateSandbox(w http.ResponseWriter, r *http.Request) { s.notImplemented(w, r) }

// DestroySandbox is implemented by its later Phase 1 task.
func (s *Server) DestroySandbox(w http.ResponseWriter, r *http.Request, _ string) {
	s.notImplemented(w, r)
}

// GetSandbox is implemented by its later Phase 1 task.
func (s *Server) GetSandbox(w http.ResponseWriter, r *http.Request, _ string) {
	s.notImplemented(w, r)
}

// Exec is implemented by its later Phase 1 task.
func (s *Server) Exec(w http.ResponseWriter, r *http.Request, _ string) { s.notImplemented(w, r) }

// ExecStream is implemented by its later Phase 1 task.
func (s *Server) ExecStream(w http.ResponseWriter, r *http.Request, _ string) {
	s.notImplemented(w, r)
}

// DeleteFile is implemented by its later Phase 1 task.
func (s *Server) DeleteFile(w http.ResponseWriter, r *http.Request, _ string, _ gen.DeleteFileParams) {
	s.notImplemented(w, r)
}

// ReadFile is implemented by its later Phase 1 task.
func (s *Server) ReadFile(w http.ResponseWriter, r *http.Request, _ string, _ gen.ReadFileParams) {
	s.notImplemented(w, r)
}

// WriteFile is implemented by its later Phase 1 task.
func (s *Server) WriteFile(w http.ResponseWriter, r *http.Request, _ string, _ gen.WriteFileParams) {
	s.notImplemented(w, r)
}

// DownloadArchive is implemented by its later Phase 1 task.
func (s *Server) DownloadArchive(w http.ResponseWriter, r *http.Request, _ string, _ gen.DownloadArchiveParams) {
	s.notImplemented(w, r)
}

// UploadArchive is implemented by its later Phase 1 task.
func (s *Server) UploadArchive(w http.ResponseWriter, r *http.Request, _ string, _ gen.UploadArchiveParams) {
	s.notImplemented(w, r)
}

// ListFiles is implemented by its later Phase 1 task.
func (s *Server) ListFiles(w http.ResponseWriter, r *http.Request, _ string, _ gen.ListFilesParams) {
	s.notImplemented(w, r)
}

// KeepaliveSandbox is implemented by its later Phase 1 task.
func (s *Server) KeepaliveSandbox(w http.ResponseWriter, r *http.Request, _ string) {
	s.notImplemented(w, r)
}
