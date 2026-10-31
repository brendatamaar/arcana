// Command clean removes local build artifacts.
package main

import (
	"log"
	"os"
)

func main() {
	if err := os.RemoveAll("bin"); err != nil {
		log.Fatal(err)
	}
}
