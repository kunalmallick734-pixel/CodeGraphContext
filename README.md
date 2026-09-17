# CodeGraphContext MCP Server

CodeGraphContext is a local Model Context Protocol (MCP) server written in Go that extracts structural call graphs from Python codebases and persists them into a queryable SQLite database. Traditional AI assistants rely on flat lexical search or context-window stuffing, which lacks structural awareness across multi-file codebases. CodeGraphContext resolves this by parsing code with Tree-sitter concrete syntax trees (CST) and exposing a standardized MCP tool interface over stdio JSON-RPC, enabling AI coding agents to perform exact caller-hierarchy queries and structural impact analysis.

## Features

- **AST-Based Syntax Parsing**: Uses Tree-sitter via `go-tree-sitter` and the Python grammar to parse source code into abstract syntax trees, capturing accurate function boundaries with byte offsets (`start_byte`, `end_byte`).
- **Call Relationship Extraction**: Extracts both direct function calls and object method invocations (`self.method()`) using Tree-sitter S-expression queries, resolving caller identity through AST parent-pointer traversal.
- **Relational Graph Storage**: Persists code entities and directed relationships in SQLite (`nodes` and `edges` tables) with foreign-key constraints, cascading deletes, and indexes on lookup keys.
- **Concurrent Producer-Consumer Pipeline**: Decouples directory discovery from parsing using a buffered file channel (`filesChan`, capacity 100) consumed by a pool of 4 concurrent worker goroutines.
- **Serialized Write Queue**: Routes all database mutation operations through a dedicated background writer channel (`writeChan`, capacity 1000) to guarantee sequential SQLite writes and prevent `SQLITE_BUSY` database lock collisions.
- **Synchronization Barriers**: Implements `sync.WaitGroup` tracking and a `Flush()` barrier to guarantee that background writes are fully committed before read queries execute.
- **MCP Server Interface**: Exposes a `get_callers` tool adhering to the Model Context Protocol specification over stdio JSON-RPC using `mark3labs/mcp-go`.
- **On-Demand Indexing**: Automatically discovers whether `graph.db` exists in the requested project path, builds the relational index on first request, and reuses the database for subsequent queries.
- **Stdio Isolation**: Routes all diagnostic and worker progress logs to `os.Stderr`, preserving `os.Stdout` exclusively for clean JSON-RPC frame communication.

## Architecture

The system operates as a pipelined workflow: file discovery feeds concurrent AST workers, which extract structural entities and dispatch mutations to a single-threaded database writer.

```text
                        +----------------------------+
                        |   MCP Client (IDE / AI)    |
                        +--------------+-------------+
                                       | JSON-RPC (stdio)
                                       v
+-----------------------------------------------------------------------+
| CodeGraphContext MCP Server                                           |
|                                                                       |
|  [Tool Handler: get_callers]                                          |
|         |                                                             |
|         +-- (If graph.db missing)                                     |
|         |                                                             |
|         v                                                             |
|   +---------------+                                                   |
|   | Directory     |                                                   |
|   | Crawler       |                                                   |
|   +-------+-------+                                                   |
|           |                                                           |
|           | filesChan (buffered, cap=100)                             |
|           v                                                           |
|   +------------------------------------+                              |
|   | Worker Pool (4 Goroutines)         |                              |
|   | - Read source file                 |                              |
|   | - Tree-sitter AST parse & query    |                              |
|   | - Extract function definitions     |                              |
|   | - Resolve calls via parent AST walk|                              |
|   +-------------------+----------------+                              |
|                       |                                               |
|                       | writeChan (buffered, cap=1000)                |
|                       v                                               |
|   +------------------------------------+                              |
|   | SQLite Writer Goroutine            |                              |
|   | - Serialized UpsertNode/UpsertEdge |                              |
|   | - sync.WaitGroup tracking          |                              |
|   +-------------------+----------------+                              |
|                       |                                               |
|                       v WAL Mode                                      |
|   +------------------------------------+                              |
|   | SQLite Database (graph.db)         |                              |
|   | - nodes (id, type, name, offsets)  |                              |
|   | - edges (source_id, target_id)     |                              |
|   +-------------------+----------------+                              |
|                       |                                               |
|         +-------------+                                               |
|         | Flush() wait barrier                                        |
|         v                                                             |
|  [db.GetCallers(abstract:<target>)] ----------------------------------+
+-----------------------------------------------------------------------+
```

### Execution Flow

1. **Client Request**: An AI client invokes `get_callers` with `project_path` and `target_function`.
2. **File Discovery**: `crawler.Walk` traverses the filesystem with `filepath.WalkDir`, skipping directories like `.git`, `node_modules`, `vendor`, and `.venv`, and pushing valid paths into `filesChan`.
3. **AST Parsing**: A pool of 4 worker goroutines reads file contents and passes byte slices to `parser.ParsePython`.
4. **Relationship Extraction**: Tree-sitter S-expression queries identify functions and call expressions. Call sites traverse parent AST nodes to identify their containing function.
5. **Channel-Based Storage**: Discovered nodes and edges are queued as closures into `writeChan`. The single writer goroutine executes upserts into SQLite under WAL mode.
6. **Flush Barrier**: `db.Flush()` blocks until the `sync.WaitGroup` counter reaches zero, ensuring all writes are committed.
7. **Graph Query**: `db.GetCallers` queries the `edges` table for callers of the target function and returns formatted results over stdio.

## Tech Stack

| Component | Technology | Role |
|---|---|---|
| Language | Go 1.26+ | Core application runtime and concurrency management |
| Protocol | Model Context Protocol (MCP) | JSON-RPC communication via `github.com/mark3labs/mcp-go` |
| AST Engine | Tree-sitter (`smacker/go-tree-sitter`) | Concrete syntax tree generation and S-expression queries |
| Grammar | `smacker/go-tree-sitter/python` | Python grammar bindings for Tree-sitter |
| Storage | SQLite (`modernc.org/sqlite`) | CGO-free, pure-Go embedded relational database engine |
| Concurrency | Go Channels & `sync.WaitGroup` | Worker pool dispatch, serialized write queue, and barrier synchronization |

## How It Works

### AST Parsing & Symbol Extraction
`pkg/parser/python.go` initializes a Tree-sitter parser with the Python grammar and builds an AST from raw source bytes.

1. **Function Definitions**: Evaluates an S-expression query:
   ```query
   (function_definition
       name: (identifier) @func.name) @func.def
   ```
   Captures the identifier name and the node's byte boundaries (`StartByte`, `EndByte`), creating a node record.
2. **Call Sites & Method Invocations**: Uses query alternation to detect direct calls and member method calls:
   ```query
   (call
       function: [
           (identifier) @callee
           (attribute
               attribute: (identifier) @callee)
       ])
   ```
3. **Caller Resolution via Parent Traversal**: Rather than attempting complex scope modeling in Tree-sitter queries, the parser takes the matched call AST node and traverses parent pointers (`node.Parent()`) upwards until it encounters a `function_definition` node, extracting the enclosing function's name.

### Graph Representation & Relational Schema
Entities are stored in SQLite using a normalized schema with two core tables:

```sql
CREATE TABLE IF NOT EXISTS nodes (
    id TEXT PRIMARY KEY,
    type TEXT NOT NULL,
    name TEXT NOT NULL,
    file_path TEXT NOT NULL,
    start_byte INTEGER,
    end_byte INTEGER
);

CREATE TABLE IF NOT EXISTS edges (
    source_id TEXT NOT NULL,
    target_id TEXT NOT NULL,
    type TEXT NOT NULL,
    PRIMARY KEY (source_id, target_id, type),
    FOREIGN KEY (source_id) REFERENCES nodes(id) ON DELETE CASCADE,
    FOREIGN KEY (target_id) REFERENCES nodes(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_nodes_file_path ON nodes(file_path);
CREATE INDEX IF NOT EXISTS idx_edges_target ON edges(target_id);
```

- **Deterministic Node IDs**: Local functions are stored as `<file_path>:<function_name>`.
- **Abstract Callee Resolution**: Invocations whose definitions may reside in other modules or external packages are stored as `abstract:<callee_name>`. A stub node is upserted to maintain relational integrity against foreign key constraints.
- **Edges**: Directed relationships (`source_id` -> `target_id`) with type `calls`.

### Concurrency & Thread-Safe SQLite Writes
SQLite is optimized for single-writer concurrency; concurrent writes from multiple worker threads can trigger `SQLITE_BUSY` errors.

- **Worker Pool**: 4 worker goroutines consume file paths from `filesChan` (buffer capacity 100) in parallel, handling disk I/O and CPU-bound AST parsing.
- **Dedicated Writer Channel**: All database operations (`UpsertNode`, `UpsertEdge`) wrap SQL execution into parameterless closures (`func()`) pushed to `writeChan` (buffer capacity 1000). A single background goroutine reads sequentially from this channel.
- **Write-Ahead Logging (WAL)**: The database connection enables `_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)`, allowing non-blocking reads while the writer thread commits transactions.
- **Flush Synchronization**: When crawling completes, `db.Flush()` invokes `db.writeWg.Wait()`. This execution barrier guarantees that all queued writes have executed before any query runs.

### MCP Request Lifecycle
`pkg/mcp_server/server.go` configures the `graphContext` MCP server using `mark3labs/mcp-go`.

- Registers the `get_callers` tool with two parameters: `project_path` (string) and `target_function` (string).
- Resolves the database file location dynamically as `filepath.Join(projectPath, "graph.db")`.
- If `graph.db` does not exist, triggers `crawlAndParse(projectPath, db)` and flushes the write queue.
- Queries `edges` via `db.GetCallers("abstract:" + targetFunc)` and returns the list of calling functions to the client.
- All diagnostic and log output is sent to `os.Stderr`, ensuring the stdio JSON-RPC stream remains uncorrupted.

## Project Structure

```text
CodeGraphContext/
├── main.go               # Application entrypoint; initializes and serves the MCP server over stdio
├── demo.py               # Sample Python file defining caller and target functions for testing
├── go.mod                # Go module definition and direct/indirect dependency declarations
├── go.sum                # Checksums for module dependencies
├── LICENSE.txt           # Apache 2.0 open-source license
├── README.md             # Project architecture and technical documentation
└── pkg/
    ├── crawler/
    │   └── walker.go     # Concurrent directory crawler with directory ignore lists and extension filters
    ├── parser/
    │   └── python.go     # Tree-sitter AST queries, function definition indexing, and caller resolution
    ├── storage/
    │   ├── sqlite.go     # SQLite initialization (WAL mode), schema definition, and serialized write queue
    │   └── crud.go       # Node/edge upsert operations, callers querying, and data models
    └── mcp_server/
        └── server.go     # MCP server lifecycle, get_callers tool handler, and worker pool coordination
```

## License

This project is licensed under the Apache License 2.0. See [LICENSE.txt](LICENSE.txt) for the full license text.
