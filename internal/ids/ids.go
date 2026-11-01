// Package ids creates opaque resource identifiers.
package ids

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"io"
	"strings"
)

// New returns a prefixed ID with 100 bits of cryptographic randomness.
func New(prefix string) (string, error) { return generate(rand.Reader, prefix) }

func generate(source io.Reader, prefix string) (string, error) {
	switch prefix {
	case "sbx", "exe", "snp", "key", "aud", "ses", "req":
	default:
		return "", fmt.Errorf("unsupported ID prefix %q", prefix)
	}
	var b [13]byte
	if _, err := io.ReadFull(source, b[:]); err != nil {
		return "", fmt.Errorf("generating ID: %w", err)
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])
	return prefix + "_" + strings.ToLower(encoded[:20]), nil
}
