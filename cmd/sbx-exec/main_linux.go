// Command sbx-exec supervises one sandbox command and its adopted descendants. The
// implementation lives in internal/sbxexec so the envd daemon can share it.
package main

import (
	"os"

	"github.com/brendatamaar/arcana/internal/sbxexec"
)

func main() {
	os.Exit(sbxexec.Main(os.Args[1:], os.Stderr))
}
