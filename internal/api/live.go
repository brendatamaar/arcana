package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/brendatamaar/arcana/internal/api/gen"
	"github.com/brendatamaar/arcana/internal/apierr"
	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/events"
	"github.com/brendatamaar/arcana/internal/store/db"
)

const (
	defaultHeartbeat = 15 * time.Second
	sseWriteTimeout  = 10 * time.Second
	// sseChunk bounds one output event so a big ring is sent as several small events.
	sseChunk = 16 << 10
)

// secureCookie decides the Secure attribute (FR-WD-4): always, except for plain HTTP to loopback.
func secureCookie(r *http.Request) bool {
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		return true
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}

func sessionCookie(r *http.Request, value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: auth.SessionCookie, Value: value, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: secureCookie(r), MaxAge: maxAge} // #nosec G124 -- Secure is mandatory except for plain HTTP to loopback (PRD FR-WD-4)
}

// CreateSession exchanges an API key for a browser session cookie.
func (s *Server) CreateSession(w http.ResponseWriter, r *http.Request) {
	if !s.keys.SessionsEnabled() {
		s.failure(w, r, apierr.NewForbidden("dashboard sessions are disabled"))
		return
	}
	p, ok := auth.FromContext(r.Context())
	if !ok {
		s.failure(w, r, apierr.NewUnauthorized("authentication required"))
		return
	}
	if _, viaCookie := auth.SessionFromContext(r.Context()); viaCookie {
		s.failure(w, r, apierr.NewForbidden("a session cannot create sessions; use an API key"))
		return
	}
	token, session, err := s.keys.CreateSession(r.Context(), p)
	if err != nil {
		s.failure(w, r, apierr.NewInternal("creating session failed"))
		return
	}
	http.SetCookie(w, sessionCookie(r, token, int(s.keys.SessionTTL().Seconds())))
	w.Header().Set("Cache-Control", "no-store")
	s.respond(w, r, http.StatusCreated, gen.Session{Id: session.ID, ExpiresAt: session.ExpiresAt})
}

// DeleteCurrentSession ends the cookie's session and clears the cookie.
func (s *Server) DeleteCurrentSession(w http.ResponseWriter, r *http.Request) {
	if session, ok := auth.SessionFromContext(r.Context()); ok {
		if err := s.keys.EndSession(r.Context(), session.ID); err != nil {
			s.failure(w, r, apierr.NewInternal("ending session failed"))
			return
		}
	}
	http.SetCookie(w, sessionCookie(r, "", -1))
	w.WriteHeader(http.StatusNoContent)
}

// sse writes Server-Sent Events with per-write deadlines so a stalled client cannot pin a goroutine.
type sse struct {
	w          http.ResponseWriter
	controller *http.ResponseController
}

func newSSE(w http.ResponseWriter) (*sse, bool) {
	controller := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	out := &sse{w: w, controller: controller}
	return out, out.flush() == nil
}

func (e *sse) flush() error {
	_ = e.controller.SetWriteDeadline(time.Now().Add(sseWriteTimeout))
	return e.controller.Flush()
}

func (e *sse) event(id uint64, name string, data []byte) error {
	_ = e.controller.SetWriteDeadline(time.Now().Add(sseWriteTimeout))
	var b strings.Builder
	if id != 0 {
		fmt.Fprintf(&b, "id: %d\n", id)
	}
	fmt.Fprintf(&b, "event: %s\ndata: %s\n\n", name, data)
	if _, err := e.w.Write([]byte(b.String())); err != nil {
		return err
	}
	return e.flush()
}

func (e *sse) comment(text string) error {
	_ = e.controller.SetWriteDeadline(time.Now().Add(sseWriteTimeout))
	if _, err := fmt.Fprintf(e.w, ": %s\n\n", text); err != nil {
		return err
	}
	return e.flush()
}

func (s *Server) heartbeatInterval() time.Duration {
	if s.heartbeat > 0 {
		return s.heartbeat
	}
	return defaultHeartbeat
}

// StreamEvents streams lifecycle, exec and pool events, filtered to what the caller may see.
func (s *Server) StreamEvents(w http.ResponseWriter, r *http.Request, params gen.StreamEventsParams) {
	if s.lifecycle == nil || s.lifecycle.Events() == nil {
		s.notImplemented(w, r)
		return
	}
	p, _ := auth.FromContext(r.Context())
	resume, last := false, uint64(0)
	if params.LastEventID != nil && *params.LastEventID != "" {
		id, err := strconv.ParseUint(*params.LastEventID, 10, 64)
		if err != nil {
			s.failure(w, r, apierr.NewInvalidRequest("Last-Event-ID must be a non-negative integer"))
			return
		}
		resume, last = true, id
	}
	sub, err := s.lifecycle.Events().Subscribe(events.ForPrincipal(p.ID, p.Admin), resume, last)
	if err != nil {
		s.failure(w, r, apierr.NewQuotaExceeded("too many event streams"))
		return
	}
	defer sub.Close()
	out, ok := newSSE(w)
	if !ok {
		return
	}
	if sub.ResetNow {
		_ = out.event(0, "reset", []byte("{}"))
		return
	}
	send := func(e events.Event) bool {
		data, _ := json.Marshal(e)
		return out.event(e.ID, e.Type, data) == nil
	}
	for _, e := range sub.Replay {
		if !send(e) {
			return
		}
	}
	ticker := time.NewTicker(s.heartbeatInterval())
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e, open := <-sub.Live():
			if !open {
				if sub.Dropped() {
					_ = out.event(0, "reset", []byte("{}"))
				}
				return
			}
			if !send(e) {
				return
			}
		case <-ticker.C:
			if out.comment("keep-alive") != nil {
				return
			}
		}
	}
}

func execRecord(row db.Exec) gen.ExecRecord {
	rec := gen.ExecRecord{Id: row.ID, SandboxId: row.SandboxID, Cwd: row.Cwd.String, Tty: row.Tty == 1, StartedAt: row.StartedAt, Cancelled: row.Cancelled.Int64 == 1}
	// The execs table stores argv as JSON and a shell string verbatim, flagged by is_shell.
	if row.IsShell == 1 {
		shell := row.Command
		rec.Shell = &shell
	} else {
		var argv []string
		if json.Unmarshal([]byte(row.Command), &argv) == nil {
			rec.Cmd = &argv
		}
	}
	if row.EndedAt.Valid {
		rec.EndedAt = &row.EndedAt.Time
	}
	if row.ExitCode.Valid {
		code := int(row.ExitCode.Int64)
		rec.ExitCode = &code
	}
	if row.TimedOut.Valid {
		v := row.TimedOut.Int64 == 1
		rec.TimedOut = &v
	}
	if row.Truncated.Valid {
		v := row.Truncated.Int64 == 1
		rec.Truncated = &v
	}
	if row.StdoutBytes.Valid {
		rec.StdoutBytes = &row.StdoutBytes.Int64
	}
	if row.StderrBytes.Valid {
		rec.StderrBytes = &row.StderrBytes.Int64
	}
	return rec
}

// ListExecs returns a page of a sandbox's exec metadata, newest first.
func (s *Server) ListExecs(w http.ResponseWriter, r *http.Request, id string, params gen.ListExecsParams) {
	if s.lifecycle == nil {
		s.notImplemented(w, r)
		return
	}
	limit, cursor := int64(100), ""
	if params.Limit != nil {
		limit = *params.Limit
	}
	if params.Cursor != nil {
		cursor = *params.Cursor
	}
	p, _ := auth.FromContext(r.Context())
	rows, err := s.lifecycle.ListExecs(r.Context(), p, id, limit, cursor)
	if err != nil {
		s.sandboxFailure(w, r, err)
		return
	}
	out := gen.ExecList{Items: make([]gen.ExecRecord, 0, len(rows))}
	for _, row := range rows {
		out.Items = append(out.Items, execRecord(row))
	}
	if int64(len(rows)) == limit {
		next := rows[len(rows)-1].ID
		out.NextCursor = &next
	}
	s.respond(w, r, http.StatusOK, out)
}

// GetOverview returns the dashboard landing data.
func (s *Server) GetOverview(w http.ResponseWriter, r *http.Request) {
	if s.lifecycle == nil {
		s.notImplemented(w, r)
		return
	}
	p, _ := auth.FromContext(r.Context())
	overview, err := s.lifecycle.Overview(r.Context(), p)
	if err != nil {
		s.sandboxFailure(w, r, err)
		return
	}
	out := gen.Overview{Version: s.version, Runtime: overview.Runtime, SandboxesByTemplate: []gen.TemplateCount{}, Pool: []gen.PoolFill{}, RecentTerminations: []gen.Termination{}}
	for _, c := range overview.ActiveByTemplate {
		out.SandboxesByTemplate = append(out.SandboxesByTemplate, gen.TemplateCount{Template: c.Template, Count: c.Count})
	}
	for _, c := range overview.Pool {
		out.Pool = append(out.Pool, gen.PoolFill{Template: c.Template, Available: c.Count, Target: int64(c.Target)})
	}
	for _, row := range overview.Recent {
		t := gen.Termination{Id: row.ID, Template: row.Template, State: gen.SandboxState(row.State), EndedAt: row.EndedAt.Time}
		if row.EndReason.Valid {
			t.Reason = &row.EndReason.String
		}
		out.RecentTerminations = append(out.RecentTerminations, t)
	}
	s.respond(w, r, http.StatusOK, out)
}

type endPayload struct {
	ExitCode  *int `json:"exit_code"`
	TimedOut  bool `json:"timed_out"`
	Truncated bool `json:"truncated"`
	Cancelled bool `json:"cancelled"`
	Expired   bool `json:"expired"`
}

// StreamExecOutput replays an exec's buffered output, then follows it until the exec ends.
func (s *Server) StreamExecOutput(w http.ResponseWriter, r *http.Request, id, execID string) {
	if s.lifecycle == nil {
		s.notImplemented(w, r)
		return
	}
	p, _ := auth.FromContext(r.Context())
	tail, row, err := s.lifecycle.ExecOutput(r.Context(), p, id, execID)
	if err != nil {
		s.sandboxFailure(w, r, err)
		return
	}
	var reader *events.TailSub
	if tail != nil {
		if reader, err = tail.Subscribe(); err != nil {
			s.failure(w, r, apierr.NewQuotaExceeded("too many output readers for this exec"))
			return
		}
		defer reader.Release()
	}
	out, ok := newSSE(w)
	if !ok {
		return
	}
	if reader == nil {
		// The ring has been dropped (or the exec predates this server run): report the record.
		end := endPayload{TimedOut: row.TimedOut.Int64 == 1, Truncated: row.Truncated.Int64 == 1, Cancelled: row.Cancelled.Int64 == 1, Expired: true}
		if row.ExitCode.Valid {
			code := int(row.ExitCode.Int64)
			end.ExitCode = &code
		}
		data, _ := json.Marshal(end)
		_ = out.event(0, "end", data)
		return
	}
	for {
		waitCtx, cancel := context.WithTimeout(r.Context(), s.heartbeatInterval())
		batch, err := reader.Next(waitCtx)
		cancel()
		if err != nil {
			if r.Context().Err() != nil {
				return
			}
			if errors.Is(err, context.DeadlineExceeded) {
				if out.comment("keep-alive") != nil {
					return
				}
				continue
			}
			return
		}
		if batch.Dropped {
			if out.event(0, "gap", []byte("{}")) != nil {
				return
			}
		}
		for _, chunk := range batch.Chunks {
			stream := "stdout"
			if chunk.Stderr {
				stream = "stderr"
			}
			for offset := 0; offset < len(chunk.Data); offset += sseChunk {
				end := min(len(chunk.Data), offset+sseChunk)
				data, _ := json.Marshal(map[string]any{"seq": chunk.Seq, "stream": stream, "data": base64.StdEncoding.EncodeToString(chunk.Data[offset:end])})
				if out.event(0, "output", data) != nil {
					return
				}
			}
		}
		if batch.End != nil {
			payload := endPayload{TimedOut: batch.End.TimedOut, Truncated: batch.End.Truncated, Cancelled: batch.End.Cancelled}
			if batch.End.Known {
				code := batch.End.ExitCode
				payload.ExitCode = &code
			}
			data, _ := json.Marshal(payload)
			_ = out.event(0, "end", data)
			return
		}
	}
}

// GetSandboxHistory returns a sandbox's state transitions.
func (s *Server) GetSandboxHistory(w http.ResponseWriter, r *http.Request, id string) {
	if s.lifecycle == nil {
		s.notImplemented(w, r)
		return
	}
	p, _ := auth.FromContext(r.Context())
	rows, err := s.lifecycle.History(r.Context(), p, id)
	if err != nil {
		s.sandboxFailure(w, r, err)
		return
	}
	out := gen.StateHistory{Items: make([]gen.StateChange, 0, len(rows))}
	for _, row := range rows {
		change := gen.StateChange{State: gen.SandboxState(row.State), At: row.At}
		if row.Reason.Valid {
			change.Reason = &row.Reason.String
		}
		out.Items = append(out.Items, change)
	}
	s.respond(w, r, http.StatusOK, out)
}
