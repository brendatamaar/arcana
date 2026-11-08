package api

import (
	"encoding/json"
	"testing"

	"github.com/brendatamaar/arcana/internal/api/gen"
)

func TestTemplates_AuthenticationAndSharedRegistry(t *testing.T) {
	handler, admin, owner, other := lifecycleFixture(t)
	if got := request(handler, "GET", "/v1/templates", "", ""); got.Code != 401 {
		t.Fatal(got.Code)
	}
	for _, key := range []string{admin, owner, other} {
		got := request(handler, "GET", "/v1/templates", key, "")
		if got.Code != 200 {
			t.Fatal(got.Code, got.Body.String())
		}
		var list gen.TemplateList
		if err := json.Unmarshal(got.Body.Bytes(), &list); err != nil {
			t.Fatal(err)
		}
		if len(list.Items) != 1 || list.Items[0].Name != "base" || list.Items[0].TtlSeconds != 1800 || list.Items[0].NetworkMode != "none" {
			t.Fatalf("%+v", list)
		}
	}
}
