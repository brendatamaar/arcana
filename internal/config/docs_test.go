package config

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// keyPaths lists every configurable dotted key path of a struct type, using
// koanf tags. Slices of structs use "[]" and maps are leaves.
func keyPaths(t reflect.Type, prefix string) []string {
	var paths []string
	for i := range t.NumField() {
		field := t.Field(i)
		name, opts, _ := strings.Cut(field.Tag.Get("koanf"), ",")
		squash := opts == "squash"
		if name == "" && !squash {
			continue
		}
		path := prefix
		if !squash {
			path = prefix + name
		}
		ft := field.Type
		suffix := "."
		if ft.Kind() == reflect.Slice && ft.Elem().Kind() == reflect.Struct {
			ft, suffix = ft.Elem(), "[]."
		}
		if ft.Kind() == reflect.Struct {
			if squash {
				paths = append(paths, keyPaths(ft, prefix)...)
			} else {
				paths = append(paths, keyPaths(ft, path+suffix)...)
			}
			continue
		}
		paths = append(paths, path)
	}
	return paths
}

func TestEveryConfigKeyIsDocumented(t *testing.T) {
	raw, err := os.ReadFile("../../docs/configuration.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	paths := keyPaths(reflect.TypeOf(Config{}), "")
	if len(paths) < 50 {
		t.Fatalf("key discovery found only %d keys", len(paths))
	}
	for _, path := range paths {
		if !strings.Contains(doc, "`"+path+"`") {
			t.Errorf("docs/configuration.md does not document `%s`", path)
		}
	}
}
