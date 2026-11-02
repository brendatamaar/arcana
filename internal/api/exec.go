package api

import (
	"net/http"

	"github.com/brendatamaar/arcana/internal/api/gen"
	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/orchestrator"
)

// Explicit wire decoding retains unknown-field rejection; the generated oneOf
// decoder preserves unknown properties in its raw union instead.
type execBody struct {
	Cmd            *[]string         `json:"cmd"`
	Shell          *string           `json:"shell"`
	Cwd            *string           `json:"cwd"`
	Env            map[string]string `json:"env"`
	Stdin          []byte            `json:"stdin"`
	TimeoutSeconds *int64            `json:"timeout_seconds"`
}

// Exec returns a supervised command's bounded, valid UTF-8 output and real exit code.
func (s *Server) Exec(w http.ResponseWriter, r *http.Request, id string) {
	if s.lifecycle == nil {
		s.notImplemented(w, r)
		return
	}
	var body execBody
	if !s.decode(w, r, &body) {
		return
	}
	p, _ := auth.FromContext(r.Context())
	result, err := s.lifecycle.Exec(r.Context(), p, id, orchestrator.ExecRequest{Cmd: body.Cmd, Shell: body.Shell, Cwd: body.Cwd, Env: body.Env, Stdin: body.Stdin, TimeoutSeconds: body.TimeoutSeconds})
	if err != nil {
		s.sandboxFailure(w, r, err)
		return
	}
	s.respond(w, r, 200, gen.ExecResult{ExecId: result.ID, ExitCode: result.ExitCode, Stdout: result.Stdout, Stderr: result.Stderr, DurationMs: result.DurationMS, TimedOut: result.TimedOut, Truncated: result.Truncated})
}
