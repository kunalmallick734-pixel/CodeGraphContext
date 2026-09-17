package parser

import (
	"context"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/python"
	"github.com/vedansh-5/graphcontext/pkg/storage"
)

// ParsePython takes a file path, its source code, and parses it into our Database.
func ParsePython(filePath string, sourceCode []byte, db *storage.DB) error {
	// Initialize the parser with python grammar
	parser := sitter.NewParser()
	parser.SetLanguage(python.GetLanguage())

	// Parse the source code into an Abstract syntax tree (AST)
	tree, err := parser.ParseCtx(context.Background(), nil, sourceCode)
	if err != nil {
		return err
	}
	rootNode := tree.RootNode()

	// scheme-like query to find all python functions.
	// we capture two things: the whole definition (@func.def) for byte boundaries,
	// and the identifier (@func.name) for the actual string name.
	queryStr := `
		(function_definition
			name: (identifier) @func.name) @func.def
	`
	q, err := sitter.NewQuery([]byte(queryStr), python.GetLanguage())
	if err != nil {
		return err
	}

	// execute the query against the root of our AST
	qc := sitter.NewQueryCursor()
	qc.Exec(q, rootNode)

	// iterate over all matches found in the file
	for {
		m, ok := qc.NextMatch()
		if !ok {
			break
		}
		var funcName string
		var startByte, endByte uint32

		// Extract the tags we defined in our queryStr
		for _, c := range m.Captures {
			captureName := q.CaptureNameForId(c.Index)

			if captureName == "func.name" {
				funcName = c.Node.Content(sourceCode)
			} else if captureName == "func.def" {
				startByte = c.Node.StartByte()
				endByte = c.Node.EndByte()
			}
		}

		// save the discovered function as a node in sqlite
		if funcName != "" {
			n := storage.Node{
				ID:        filePath + ":" + funcName, // Deterministic unique ID
				Type:      "function",
				Name:      funcName,
				FilePath:  filePath,
				StartByte: int(startByte),
				EndByte:   int(endByte),
			}
			// In a production app, we would batch these inserts!
			db.UpsertNode(n)
		}
	}

	// Graph Linking - Extracting Call edges

	// Query to find function calls inside function definitions.
	// We capture the containing functions as @caller and hte invoked functionas @callee.

	edgeQueryStr := `
	(call
			function: [
					(identifier) @callee
					(attribute
					  	attribute : (identifier) @callee)
			])
	`
	eq, err := sitter.NewQuery([]byte(edgeQueryStr), python.GetLanguage())
	if err != nil {
		return err
	}
	eqc := sitter.NewQueryCursor()
	eqc.Exec(eq, rootNode)

	for {
		m, ok := eqc.NextMatch()
		if !ok {
			break
		}

		var calleeName string
		var callNode *sitter.Node

		for _, c := range m.Captures {
			captureName := eq.CaptureNameForId(c.Index)
			if captureName == "callee" {
				calleeName = c.Node.Content(sourceCode)
				callNode = c.Node
			}
		}

		if calleeName != "" && callNode != nil {
			callerName := getContainingFunction(callNode, sourceCode)
			if callerName != "" {
				// the caller is in the list so we know its exact id
				sourceID := filePath + ":" + callerName
				// we might not know which file the callee is in yet, so we create a global abstract ID for the target.
				targetID := "abstract:" + calleeName
				// insert a stub node so the foreign key constraints don't fail

				db.UpsertNode(storage.Node{
					ID:       targetID,
					Type:     "function",
					Name:     calleeName,
					FilePath: "unknown",
				})

				// create the directed edge - caller to callee
				db.UpsertEdge(storage.Edge{
					SourceID: sourceID,
					TargetID: targetID,
					Type:     "calls",
				})
			}
		}
	}

	return nil
}

// traverse the AST to find the parent function_definition
func getContainingFunction(node *sitter.Node, sourceCode []byte) string {
	curr := node.Parent()
	for curr != nil {
		if curr.Type() == "function_definition" {
			nameNode := curr.ChildByFieldName("name")
			if nameNode != nil {
				return nameNode.Content(sourceCode)
			}
		}
		curr = curr.Parent()
	}
	return ""
}
