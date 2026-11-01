package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/arcana/internal/store"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }
func fixture(t *testing.T) (*Service, *store.Store, *fakeClock, string) {
	t.Helper()
	dir := t.TempDir()
	database, err := store.Open(context.Background(), filepath.Join(dir, "arcana.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	})
	clock := &fakeClock{time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}
	return New(database, clock), database, clock, dir
}

func TestKeys_CreateVerifyDebounceAndRevoke(t *testing.T) {
	service, database, clock, _ := fixture(t)
	ctx := context.Background()
	key, raw, err := service.Create(ctx, "operator", true)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^arc_live_[A-Za-z0-9]{32}$`).MatchString(raw) || len(key.Prefix) != 8 {
		t.Fatalf("invalid key format: %+v", key)
	}
	row, err := database.Queries.GetKey(ctx, key.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(row.Hash) != 32 || bytes.Contains(row.Hash, []byte(raw)) {
		t.Fatal("credential was not hashed")
	}
	principal, err := service.Verify(ctx, raw)
	if err != nil || principal.ID != key.ID || !principal.Admin {
		t.Fatalf("verification: %+v %v", principal, err)
	}
	first, err := database.Queries.GetKey(ctx, key.ID)
	if err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(30 * time.Second)
	if _, err = service.Verify(ctx, raw); err != nil {
		t.Fatal(err)
	}
	second, err := database.Queries.GetKey(ctx, key.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !second.LastUsedAt.Time.Equal(first.LastUsedAt.Time) {
		t.Fatal("last_used not debounced")
	}
	clock.now = clock.now.Add(31 * time.Second)
	if _, err = service.Verify(ctx, raw); err != nil {
		t.Fatal(err)
	}
	third, err := database.Queries.GetKey(ctx, key.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !third.LastUsedAt.Time.Equal(clock.now) {
		t.Fatal("last_used not refreshed")
	}
	keys, err := service.List(ctx, "", 10)
	if err != nil || len(keys) != 1 {
		t.Fatalf("list: %v %v", keys, err)
	}
	encoded, err := json.Marshal(keys)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), raw) || strings.Contains(string(encoded), "hash") {
		t.Fatal("list leaked key material")
	}
	if err = service.Revoke(ctx, key.ID); err != nil {
		t.Fatal(err)
	}
	if err = service.Revoke(ctx, key.ID); err != nil {
		t.Fatal(err)
	}
	var invalid InvalidCredentials
	if _, err = service.Verify(ctx, raw); !errors.As(err, &invalid) {
		t.Fatalf("revoked key accepted: %v", err)
	}
	for _, bad := range []string{"", "arc_live_" + strings.Repeat("!", 32), "arc_live_" + strings.Repeat("x", 32), raw + "x"} {
		if _, err = service.Verify(ctx, bad); !errors.As(err, &invalid) {
			t.Fatal("invalid key accepted")
		}
	}
	if _, _, err = service.Create(ctx, "", false); err == nil {
		t.Fatal("accepted empty name")
	}
	if _, _, err = service.Create(ctx, strings.Repeat("x", 129), false); err == nil {
		t.Fatal("accepted long name")
	}
	if _, err = service.List(ctx, "", -1); err == nil {
		t.Fatal("accepted unbounded list")
	}
}

func TestBootstrap_ExactlyOnceAndPrivateFile(t *testing.T) {
	service, database, _, dir := fixture(t)
	ctx := context.Background()
	var out bytes.Buffer
	if err := service.Bootstrap(ctx, dir, &out); err != nil {
		t.Fatal(err)
	}
	raw := strings.TrimSpace(out.String())
	principal, err := service.Verify(ctx, raw)
	if err != nil || !principal.Admin {
		t.Fatalf("bootstrap credential: %v", err)
	}
	file, err := os.ReadFile(filepath.Join(dir, "admin.key")) // #nosec G304 -- isolated t.TempDir fixture
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(file)) != raw {
		t.Fatal("file differs from output")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, "admin.key"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode: %o", info.Mode().Perm())
		}
	}
	out.Reset()
	if err = service.Bootstrap(ctx, dir, &out); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatal("bootstrap printed twice")
	}
	count, err := database.Queries.CountKeys(ctx)
	if err != nil || count != 1 {
		t.Fatalf("key count: %d %v", count, err)
	}
}

func TestBootstrap_FailureDoesNotLoseCommittedCredential(t *testing.T) {
	service, database, _, dir := fixture(t)
	ctx := context.Background()
	if err := service.Bootstrap(ctx, dir, failingWriter{}); err == nil {
		t.Fatal("output error ignored")
	}
	file, err := os.ReadFile(filepath.Join(dir, "admin.key")) // #nosec G304 -- isolated t.TempDir fixture
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Verify(ctx, strings.TrimSpace(string(file))); err != nil {
		t.Fatal(err)
	}
	count, err := database.Queries.CountKeys(ctx)
	if err != nil || count != 1 {
		t.Fatalf("key not committed: %d %v", count, err)
	}
	var out bytes.Buffer
	if err = service.Bootstrap(ctx, dir, &out); err != nil || out.Len() != 0 {
		t.Fatalf("bootstrap repeated: %v", err)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestBootstrap_RefusesExistingFileAndRollsBackInsertFailure(t *testing.T) {
	for _, existing := range []bool{true, false} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			service, database, _, dir := fixture(t)
			ctx := context.Background()
			filename := filepath.Join(dir, "admin.key")
			var out bytes.Buffer
			if existing {
				if err := os.WriteFile(filename, []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := database.SQL.ExecContext(ctx, "CREATE TRIGGER deny_key BEFORE INSERT ON api_keys BEGIN SELECT RAISE(FAIL, 'injected failure'); END"); err != nil {
					t.Fatal(err)
				}
			}
			if err := service.Bootstrap(ctx, dir, &out); err == nil {
				t.Fatal("bootstrap should fail")
			}
			count, err := database.Queries.CountKeys(ctx)
			if err != nil || count != 0 || out.Len() != 0 {
				t.Fatalf("partial bootstrap: %d %v", count, err)
			}
			if existing {
				b, err := os.ReadFile(filename) // #nosec G304 -- isolated t.TempDir fixture
				if err != nil || string(b) != "keep" {
					t.Fatal("existing file overwritten")
				}
			} else if _, err := os.Stat(filename); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("uncommitted file remains: %v", err)
			}
		})
	}
}

func TestMiddleware_HealthBearerAndAdmin(t *testing.T) {
	service, _, _, _ := fixture(t)
	ctx := context.Background()
	_, user, err := service.Create(ctx, "user", false)
	if err != nil {
		t.Fatal(err)
	}
	_, admin, err := service.Create(ctx, "admin", true)
	if err != nil {
		t.Fatal(err)
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/health" {
			if _, ok := FromContext(r.Context()); !ok {
				t.Error("missing principal")
			}
		}
		w.WriteHeader(204)
	})
	secured := service.Middleware(RequireAdmin(next))
	for _, tt := range []struct {
		header string
		want   int
	}{{"", 401}, {"Basic " + admin, 401}, {"Bearer " + user, 403}, {"Bearer " + admin, 204}} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/v1/keys", nil)
		r.Header.Set("Authorization", tt.header)
		secured.ServeHTTP(w, r)
		if w.Code != tt.want {
			t.Fatalf("status: %d want %d", w.Code, tt.want)
		}
		if strings.Contains(w.Body.String(), admin) || strings.Contains(w.Body.String(), user) {
			t.Fatal("credential leaked")
		}
	}
	w := httptest.NewRecorder()
	service.Middleware(next).ServeHTTP(w, httptest.NewRequest("GET", "/v1/health", nil))
	if w.Code != 204 {
		t.Fatal("health requires auth")
	}
	w = httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/v1/keys", nil)
	r.Header.Add("Authorization", "Bearer "+admin)
	r.Header.Add("Authorization", "Bearer "+user)
	secured.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("duplicate authorization accepted")
	}
}

func FuzzCredentialParser(f *testing.F) {
	f.Add("arc_live_" + strings.Repeat("a", 32))
	f.Add("")
	f.Fuzz(func(t *testing.T, raw string) {
		if validCredential(raw) {
			if len(raw) != 41 || raw[:9] != "arc_live_" {
				t.Fatal("invalid accepted")
			}
			for _, b := range []byte(raw[9:]) {
				if !strings.ContainsRune("0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz", rune(b)) {
					t.Fatal("non-base62 byte accepted")
				}
			}
		}
	})
}

func TestBootstrap_ConcurrentCallersCreateOneAdmin(t *testing.T) {
	service, database, _, dir := fixture(t)
	type result struct {
		output string
		err    error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			var out bytes.Buffer
			err := service.Bootstrap(context.Background(), dir, &out)
			results <- result{out.String(), err}
		}()
	}
	printed := 0
	for range 2 {
		r := <-results
		if r.err != nil {
			t.Fatal(r.err)
		}
		if r.output != "" {
			printed++
		}
	}
	count, err := database.Queries.CountKeys(context.Background())
	if err != nil || count != 1 || printed != 1 {
		t.Fatalf("count=%d printed=%d error=%v", count, printed, err)
	}
}
