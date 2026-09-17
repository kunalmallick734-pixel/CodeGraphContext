package mcp_server

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/vedansh-5/graphcontext/pkg/crawler"
	"github.com/vedansh-5/graphcontext/pkg/parser"
	"github.com/vedansh-5/graphcontext/pkg/storage"
)

// init the MCP server and blocks forever listening to stdin
func StartStdioServer() error {
	// create MCP server instance
	s := server.NewMCPServer(
		"graphContext",
		"1.0.0",
		server.WithToolCapabilities(true),
	)

	// define AI tool - get callers with dynamic project path
	getCallersTool := mcp.NewTool("get_callers",
		mcp.WithDescription("Find all functions that call a specific target function in a given project directory."),
		mcp.WithString("target_function", mcp.Required(), mcp.Description("The name of the function to find callers for")),
		mcp.WithString("project_path", mcp.Required(), mcp.Description("The absolute path of the project folder to analyze")),
	)

	// register the handler for the tool
	s.AddTool(getCallersTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, ok := request.Params.Arguments.(map[string]interface{})
		if !ok {
			return mcp.NewToolResultError("invalid arguements format"), nil
		}

		targetFunc, ok := args["target_function"].(string)
		if !ok {
			return mcp.NewToolResultError("target_function must be a string"), nil
		}

		projectPath, ok := args["project_path"].(string)
		if !ok {
			return mcp.NewToolResultError("project_path must be a string"), nil
		}

		dbPath := filepath.Join(projectPath, "graph.db")

		// check if db already exist so we dont crawl on every query
		dbExists := true
		if _, err := os.Stat(dbPath); os.IsNotExist(err) {
			dbExists = false
		}

		db, err := storage.NewDB(dbPath)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Failed to initialize database in project: %v", err)), nil
		}
		defer db.Close()

		// if this is the first time we see this project - trigger a scan
		if !dbExists {
			crawlAndParse(projectPath, db)
			db.Flush() // ensure all queued writes are flushed to SQLite before querying
		}

		// query the db
		callers, err := db.GetCallers("abstract:" + targetFunc)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		response := fmt.Sprintf("Functions calling '%s' in project %s: \n%s", targetFunc, projectPath, callers)
		return mcp.NewToolResultText(response), nil
	})

	fmt.Fprintf(os.Stderr, "Starting MCP server on stdio... \n")
	return server.ServeStdio(s)

}

// Helper to crawl the codebase and write graph info into SQLite db
func crawlAndParse(projectPath string, db *storage.DB) {
	filesChan := make(chan string, 100)

	fmt.Fprintf(os.Stderr, "Scanning dir: %s...\n", projectPath)
	crawler.Walk(projectPath, filesChan)

	var wg sync.WaitGroup
	numWorkers := 4

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for path := range filesChan {
				if strings.HasSuffix(path, ".py") {
					sourceCode, err := os.ReadFile(path)
					if err != nil {
						continue
					}
					err = parser.ParsePython(path, sourceCode, db)
					if err != nil {
						log.Printf("Worker %d: Failed to parse %s: %v", workerID, path, err)
					}
				}
			}
		}(i)
	}
	wg.Wait()
	fmt.Fprintf(os.Stderr, "Scan complete! Codebase graph saved to %s/graph.db \n", projectPath)
}
