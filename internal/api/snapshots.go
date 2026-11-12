package api

import (
	"net/http"
	"strconv"

	"github.com/brendatamaar/arcana/internal/api/gen"
	"github.com/brendatamaar/arcana/internal/apierr"
	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/orchestrator"
	"github.com/brendatamaar/arcana/internal/store/db"
)

func snapshotModel(row db.Snapshot) (gen.Snapshot, error) {
	spec, err := orchestrator.SnapshotSpecOf(row)
	if err != nil {
		return gen.Snapshot{}, err
	}
	cpus := float32(spec.CPUs)
	allow := spec.NetworkAllow
	return gen.Snapshot{
		Id: row.ID, SourceSandboxId: row.SourceSandboxID, Template: row.Template,
		Resources: gen.Resources{Cpus: &cpus, MemoryMb: &spec.MemoryMB, DiskMb: &spec.DiskMB, Pids: &spec.PIDs},
		Network:   gen.Network{Mode: gen.NetworkMode(spec.NetworkMode), Allow: &allow},
		EnvKeys:   spec.EnvKeys, SizeBytes: row.SizeBytes, Sha256: row.Sha256, CreatedAt: row.CreatedAt,
	}, nil
}

func (s *Server) respondSnapshot(w http.ResponseWriter, r *http.Request, status int, row db.Snapshot) {
	model, err := snapshotModel(row)
	if err != nil {
		s.failure(w, r, apierr.NewInternal("snapshot record is unreadable"))
		return
	}
	s.respond(w, r, status, model)
}

// CreateSnapshot archives a running sandbox's /workspace through exec.
func (s *Server) CreateSnapshot(w http.ResponseWriter, r *http.Request, id string) {
	if s.lifecycle == nil {
		s.notImplemented(w, r)
		return
	}
	p, _ := auth.FromContext(r.Context())
	row, err := s.lifecycle.CreateSnapshot(r.Context(), p, id)
	if err != nil {
		s.sandboxFailure(w, r, err)
		return
	}
	s.respondSnapshot(w, r, http.StatusCreated, row)
}

// ListSnapshots returns one bounded page of snapshots visible to the key.
func (s *Server) ListSnapshots(w http.ResponseWriter, r *http.Request, params gen.ListSnapshotsParams) {
	if s.lifecycle == nil {
		s.notImplemented(w, r)
		return
	}
	input := orchestrator.ListRequest{Limit: 100}
	if params.Limit != nil {
		input.Limit = *params.Limit
	}
	if params.Cursor != nil {
		input.Cursor = *params.Cursor
	}
	p, _ := auth.FromContext(r.Context())
	rows, err := s.lifecycle.ListSnapshots(r.Context(), p, input)
	if err != nil {
		s.sandboxFailure(w, r, err)
		return
	}
	result := gen.SnapshotList{Items: make([]gen.Snapshot, 0, len(rows))}
	for _, row := range rows {
		model, modelErr := snapshotModel(row)
		if modelErr != nil {
			s.failure(w, r, apierr.NewInternal("snapshot record is unreadable"))
			return
		}
		result.Items = append(result.Items, model)
	}
	if int64(len(rows)) == input.Limit {
		cursor := rows[len(rows)-1].ID
		result.NextCursor = &cursor
	}
	s.respond(w, r, http.StatusOK, result)
}

// GetSnapshot returns one owned snapshot.
func (s *Server) GetSnapshot(w http.ResponseWriter, r *http.Request, id string) {
	if s.lifecycle == nil {
		s.notImplemented(w, r)
		return
	}
	p, _ := auth.FromContext(r.Context())
	row, err := s.lifecycle.GetSnapshot(r.Context(), p, id)
	if err != nil {
		s.sandboxFailure(w, r, err)
		return
	}
	s.respondSnapshot(w, r, http.StatusOK, row)
}

// DeleteSnapshot removes an owned snapshot's archive and record.
func (s *Server) DeleteSnapshot(w http.ResponseWriter, r *http.Request, id string) {
	if s.lifecycle == nil {
		s.notImplemented(w, r)
		return
	}
	p, _ := auth.FromContext(r.Context())
	if err := s.lifecycle.DeleteSnapshot(r.Context(), p, id); err != nil {
		s.sandboxFailure(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ForkSnapshot creates up to snapshots.max_fork sandboxes from one snapshot.
func (s *Server) ForkSnapshot(w http.ResponseWriter, r *http.Request, id string) {
	if s.lifecycle == nil {
		s.notImplemented(w, r)
		return
	}
	var request gen.ForkRequest
	if !s.decode(w, r, &request) {
		return
	}
	p, _ := auth.FromContext(r.Context())
	if request.Count < 1 || request.Count > int64(s.maxFork) {
		s.failure(w, r, apierr.NewInvalidRequest("count must be within 1.."+strconv.Itoa(s.maxFork)))
		return
	}
	input := orchestrator.CreateRequest{TTLSeconds: request.TtlSeconds, IdleTimeoutSeconds: request.IdleTimeoutSeconds}
	if request.Env != nil {
		input.Env = *request.Env
	}
	if request.Labels != nil {
		input.Labels = *request.Labels
	}
	if request.Network != nil {
		input.NetworkMode = string(request.Network.Mode)
		if request.Network.Allow != nil {
			input.NetworkAllow = *request.Network.Allow
		}
		if request.Network.Presets != nil {
			input.NetworkPresets = *request.Network.Presets
		}
		if input.NetworkMode == "" {
			s.failure(w, r, apierr.NewInvalidRequest("network.mode is required"))
			return
		}
	}
	if request.Resources != nil {
		input.Resources = orchestrator.Overrides{MemoryMB: request.Resources.MemoryMb, DiskMB: request.Resources.DiskMb, PIDs: request.Resources.Pids}
		if request.Resources.Cpus != nil {
			value := float64(*request.Resources.Cpus)
			input.Resources.CPUs = &value
		}
	}
	// Every member is a sandbox creation, so the batch is charged before any work starts.
	if retry := s.limiter.admitN(p.ID, int(request.Count)); retry > 0 {
		w.Header().Set("Retry-After", strconv.FormatInt(retry, 10))
		s.failure(w, r, apierr.NewRateLimited("sandbox creation rate exceeded"))
		return
	}
	result, err := s.lifecycle.Fork(r.Context(), p, id, int(request.Count), input)
	if err != nil {
		s.sandboxFailure(w, r, err)
		return
	}
	if len(result.Items) == 0 && len(result.Failures) > 0 {
		s.sandboxFailure(w, r, result.Failures[0].Err)
		return
	}
	out := gen.ForkResult{Items: make([]gen.Sandbox, 0, len(result.Items)), Failures: make([]gen.ForkFailure, 0, len(result.Failures))}
	for _, row := range result.Items {
		out.Items = append(out.Items, sandboxModel(row))
	}
	for _, failure := range result.Failures {
		e := sandboxError(failure.Err)
		out.Failures = append(out.Failures, gen.ForkFailure{Index: int64(failure.Index), Code: string(e.Code), Message: e.Message})
	}
	s.respond(w, r, http.StatusCreated, out)
}
