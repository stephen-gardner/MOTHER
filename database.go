package main

import (
	"database/sql"
	"fmt"
	_ "github.com/mattn/go-sqlite3"
	"strings"
)

const (
	createMessagesTables = "CREATE TABLE IF NOT EXISTS %s_messages (id INTEGER PRIMARY KEY AUTOINCREMENT," +
		" user_id TEXT," +
		" thread_id TEXT," +
		" content TEXT," +
		" timestamp TEXT," +
		" original BOOLEAN);"
	createThreadIndexTable = "CREATE TABLE IF NOT EXISTS %s_index (" +
		"thread_id TEXT PRIMARY KEY," +
		" user_ids TEXT," +
		" timestamp DATETIME DEFAULT CURRENT_TIMESTAMP);"
	insertMessage = "INSERT INTO %s_messages" +
		" (user_id, thread_id, content, timestamp, original)" +
		" VALUES (?, ?, ?, ?, ?);"
	insertThreadIndex = "INSERT OR REPLACE INTO %s_index" +
		" (thread_id, user_ids)" +
		" VALUES (?, ?)";
)

var db *sql.DB

func openConnection(dbPath string, prefix string) error {
	var query string
	var stmt *sql.Stmt

	database, err := sql.Open("sqlite3", dbPath)
	db = database

	if err != nil {
		return err
	}

	query = fmt.Sprintf(createMessagesTables, prefix)
	stmt, err = db.Prepare(query)

	if err != nil {
		return err
	}

	defer stmt.Close()

	if _, err = stmt.Exec(); err != nil {
		return err
	}

	query = fmt.Sprintf(createThreadIndexTable, prefix)
	stmt, err = db.Prepare(query)

	if err != nil {
		return err
	}

	if _, err = stmt.Exec(); err != nil {
		return err
	}

	return nil
}

func saveMessages(prefix string, conv conversation) error {
	query := fmt.Sprintf(insertThreadIndex, prefix)
	stmt, err := db.Prepare(query)

	if (err != nil) {
		return err
	}

	defer stmt.Close()
	userIDs := strings.Join(conv.userIDs, ",")

	if _, err = stmt.Exec(conv.threadID, userIDs); err != nil {
		return err
	}

	return nil
}
