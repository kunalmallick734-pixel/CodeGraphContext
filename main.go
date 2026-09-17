package main

import (
	"log"

	"github.com/vedansh-5/graphcontext/pkg/mcp_server"
)

func main() {
	// launch MCP
	if err := mcp_server.StartStdioServer(); err != nil {
		log.Fatalf("MCP Server crashed: %v", err)
	}
}
