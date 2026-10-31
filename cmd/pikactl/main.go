// Command pikactl runs every operation of the Pika API from the command line
// and keeps a local history of what was generated.
package main

import "github.com/orshih6/pikactl/internal/cli"

func main() {
	cli.Execute()
}
