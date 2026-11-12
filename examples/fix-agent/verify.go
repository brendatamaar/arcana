package main

import (
	"bytes"
	"context"
	"fmt"

	"github.com/brendatamaar/arcana/pkg/client"
)

// VerifyResult records what a fixture does before and after its reference solution.
type VerifyResult struct {
	Fixture            string
	VisibleFailsBefore bool
	HiddenFailsBefore  bool
	VisiblePassesAfter bool
	HiddenPassesAfter  bool
	Err                error
}

// OK reports whether the fixture is a genuine, fixable bug: both suites fail on the project and
// both pass with the reference solution.
func (v VerifyResult) OK() bool {
	return v.Err == nil && v.VisibleFailsBefore && v.HiddenFailsBefore && v.VisiblePassesAfter && v.HiddenPassesAfter
}

// VerifyFixture checks a fixture in a throwaway sandbox: the visible test must expose the bug, the
// hidden tests must fail too, and the reference solution must make both suites pass.
func (d Deps) VerifyFixture(ctx context.Context, fx Fixture) VerifyResult {
	out := VerifyResult{Fixture: fx.Name}
	id, err := d.newSandbox(ctx, fx)
	if err != nil {
		out.Err = err
		return out
	}
	defer d.destroy([]string{id}, "")

	put := func(sub string) error {
		archive, archiveErr := fx.Archive(sub)
		if archiveErr != nil {
			return archiveErr
		}
		if uploadErr := d.Arcana.UploadArchive(ctx, id, "/workspace", bytes.NewReader(archive)); uploadErr != nil {
			return fmt.Errorf("uploading %s: %w", sub, uploadErr)
		}
		return nil
	}
	passes := func(command string) (bool, error) {
		timeout := int64(maxCommandSeconds)
		result, execErr := d.Arcana.Exec(ctx, id, client.ExecRequest{Shell: &command, TimeoutSeconds: &timeout})
		if execErr != nil {
			return false, execErr
		}
		return result.ExitCode == 0 && !result.TimedOut, nil
	}

	var ok bool
	if out.Err = put("project"); out.Err != nil {
		return out
	}
	if ok, out.Err = passes(fx.VisibleTest); out.Err != nil {
		return out
	}
	out.VisibleFailsBefore = !ok
	if out.Err = put("hidden"); out.Err != nil {
		return out
	}
	if ok, out.Err = passes(fx.HiddenTest); out.Err != nil {
		return out
	}
	out.HiddenFailsBefore = !ok
	if out.Err = put("solution"); out.Err != nil {
		return out
	}
	if ok, out.Err = passes(fx.VisibleTest); out.Err != nil {
		return out
	}
	out.VisiblePassesAfter = ok
	if ok, out.Err = passes(fx.HiddenTest); out.Err != nil {
		return out
	}
	out.HiddenPassesAfter = ok
	return out
}
