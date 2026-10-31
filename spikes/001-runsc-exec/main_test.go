package main

import (
	"context"
	"testing"
	"time"
)

func TestLifecycle_StreamsAndResizesUnderBothRuntimes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	results, err := run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		if len(result.Samples) != 20 || result.P50 <= 0 || result.P95 < result.P50 {
			t.Fatalf("invalid timings: %+v", result)
		}
		t.Logf("%s samples=%v p50=%.3fms p95=%.3fms", result.Runtime, result.Samples, result.P50, result.P95)
	}
}
