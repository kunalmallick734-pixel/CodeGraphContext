package storage

import (
	"fmt"
	"os"
)

type Node struct {
	ID        string
	Type      string // eg whether the node is file, function, class etc.
	Name      string
	FilePath  string
	StartByte int
	EndByte   int
}

type Edge struct {
	SourceID string
	TargetID string
	Type     string // eg contains, calls, inherits - basically all relations
}

// insert or update a node based on its ID
func (db *DB) UpsertNode(n Node) error {
	db.writeWg.Add(1)
	db.writeChan <- func() {
		query := `
		INSERT INTO nodes (id, type, name, file_path, start_byte, end_byte)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			type = excluded.type,
			name = excluded.name,
			file_path = excluded.file_path,
			start_byte = excluded.start_byte,
			end_byte = excluded.end_byte;
		`

		_, err := db.conn.Exec(query, n.ID, n.Type, n.Name, n.FilePath, n.StartByte, n.EndByte)
		if err != nil {
			fmt.Fprintf(os.Stderr, "SQLite Queue Error inserting node %s: %v\n", n.Name, err)
		}
	}
	return nil
}

// insert a new edge or quietly ignore if it already exist
func (db *DB) UpsertEdge(e Edge) error {
	db.writeWg.Add(1)
	db.writeChan <- func() {
		query := `
		INSERT INTO edges (source_id, target_id, type)
		VALUES (?,?,?)
		ON CONFLICT(source_id, target_id, type) DO NOTHING;
		`
		_, err := db.conn.Exec(query, e.SourceID, e.TargetID, e.Type)
		if err != nil {
			fmt.Fprintf(os.Stderr, "SQLite Queue Error inserting edge: %v\n", err)
		}
	}
	return nil
}

// returns a lsit of funciton IDs that call the specified target function
func (db *DB) GetCallers(targetID string) (string, error) {
	query := `SELECT source_id FROM edges WHERE target_id = ? AND type = 'calls'`
	rows, err := db.conn.Query(query, targetID)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var callers string
	for rows.Next() {
		var caller string
		if err := rows.Scan(&caller); err != nil {
			return "", err
		}
		callers += "_ " + caller + "\n"
	}

	if callers == "" {
		return "No callers found.", nil
	}
	return callers, nil
}
