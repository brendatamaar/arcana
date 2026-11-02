package main

import (
	"testing"

	"github.com/brendatamaar/arcana/internal/doctor"
)

func TestDoctor_StartupGateAcceptsWarningsAndRejectsFailures(t *testing.T) {
	for _, status := range []string{"pass", "warn", "fail"} {
		report := doctor.Report{Checks: []doctor.Result{{Name: "probe", Status: status}}}
		if err := requireHealthy(report); (err != nil) != (status == "fail") {
			t.Fatal(status, err)
		}
	}
	root := newRootCommand()
	if _, _, err := root.Find([]string{"doctor"}); err != nil {
		t.Fatal(err)
	}
}
