package storage

import (
	"database/sql"
	"fmt"
	"sync"

	_ "modernc.org/sqlite"
)

// Db wraps the sql.DB connection to proce graph-specific methods.
type DB struct {
	conn      *sql.DB
	writeChan chan func()    // channel queue for incoming write operations
	writeWg   sync.WaitGroup // waitGroup to track pending writes in the queue
}

// opens a SQLite database and ensures the schema is created.
func NewDB(dbPath string) (*DB, error) {
	// enable WAL mode for read performance and foreign keys
	dsn := fmt.Sprintf("%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)", dbPath)

	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	db := &DB{
		conn:      conn,
		writeChan: make(chan func(), 1000), // buffered queue of 1000 write ops
	}

	// span the single threaded background writer
	go func() {
		for writeOp := range db.writeChan {
			writeOp()
			db.writeWg.Done()
		}
	}()

	if err := db.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to init schema: %w", err)
	}
	return db, nil
}

// creates the core relational schema for our graph

func (db *DB) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS nodes (
		id TEXT PRIMARY KEY,
		type TEXT NOT NULL,
		name TEXT NOT NULL,
		file_path TEXT NOT NULL,
		start_byte 	INTEGER,
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
	`

	// schema creation must be run synchornously on startup
	_, err := db.conn.Exec(schema)
	return err
}

// gracefully shuts down the db connection after draining the queue
func (db *DB) Close() error {
	// close channel to stop accepting new writes and wait for queue to drain
	close(db.writeChan)
	db.writeWg.Wait()
	return db.conn.Close()
}

func (db *DB) Flush() {
	db.writeWg.Wait()
}
