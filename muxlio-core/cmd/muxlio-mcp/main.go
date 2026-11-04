// Command muxlio-mcp serves Muxlio tools over stdio or Streamable HTTP.
package main

import "github.com/muxlio/muxlio-core/internal/mcp"

var Version = "0.2.0"

func main() {
	mcp.Version = Version
	mcp.Main()
}
