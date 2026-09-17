package main

import (
	"log"

	"github.com/kunalmallick734-pixel/CodeGraphContext/pkg/mcp_server"
)

func main() {
	// launch MCP
	if err := mcp_server.StartStdioServer(); err != nil {
		log.Fatalf("MCP Server crashed: %v", err)
	}
}
