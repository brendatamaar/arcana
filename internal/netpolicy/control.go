package netpolicy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/brendatamaar/arcana/internal/runtime"
)

// controlOutputLimit bounds what a proxy may answer on its control channel.
const controlOutputLimit = 1 << 20

// Control sends one authenticated request to a proxy's private control socket. The proxy has no network
// path for it: the request runs the proxy binary's `proxy-control` helper inside the proxy container, so
// open starts that command in it. Drivers share this so every runtime speaks the same control protocol.
func Control(ctx context.Context, open func(context.Context, runtime.ExecRequest) (runtime.ExecSession, error), token, method, path string, body []byte, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	session, err := open(ctx, runtime.ExecRequest{Cmd: []string{"/arcana", "proxy-control"}})
	if err != nil {
		return err
	}
	var output bytes.Buffer
	var group errgroup.Group
	group.Go(func() error {
		_, err := io.Copy(&output, io.LimitReader(session.Stdout(), controlOutputLimit+1))
		return err
	})
	group.Go(func() error { _, err := io.Copy(io.Discard, session.Stderr()); return err })
	data, _ := json.Marshal(struct {
		Token, Method, Path string
		Body                []byte
	}{token, method, path, body})
	_, writeErr := session.Stdin().Write(data)
	closeErr := session.Stdin().Close()
	result, waitErr := session.Wait(ctx)
	drainErr := group.Wait()
	if err = errors.Join(writeErr, closeErr, waitErr, drainErr); err != nil {
		return err
	}
	if result.ExitCode != 0 || output.Len() > controlOutputLimit {
		return errors.New("proxy control failed")
	}
	if out != nil {
		return json.Unmarshal(output.Bytes(), out)
	}
	return nil
}
