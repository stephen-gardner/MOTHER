package main

import (
	"database/sql"
	"fmt"

	_ "github.com/mattn/go-sqlite3"
)

const (
	createBlacklistTable = "CREATE TABLE IF NOT EXISTS %s_blacklist" +
		" (user_id TEXT PRIMARY KEY," +
		" timestamp DATETIME DEFAULT CURRENT_TIMESTAMP);"
	createMessagesTable = "CREATE TABLE IF NOT EXISTS %s_messages" +
		" (id INTEGER PRIMARY KEY AUTOINCREMENT," +
		" user_id TEXT," +
		" thread_id TEXT," +
		" content TEXT," +
		" timestamp TEXT," +
		" original BOOLEAN);"
	createThreadIndexTable = "CREATE TABLE IF NOT EXISTS %s_index" +
		" (thread_id TEXT PRIMARY KEY," +
		" user_ids TEXT," +
		" timestamp DATETIME DEFAULT CURRENT_TIMESTAMP);"
	deleteBlacklisted = "DELETE FROM %s_blacklist" +
		" WHERE user_id = ?;"
	insertBlacklisted = "INSERT OR REPLACE INTO %s_blacklist" +
		" (user_id)" +
		" VALUES(?);"
	findThreadIndex = "SELECT user_ids FROM %s_index" +
		" WHERE thread_id = ?;"
	insertMessage = "INSERT INTO %s_messages" +
		" (user_id, thread_id, content, timestamp, original)" +
		" VALUES (?, ?, ?, ?, ?);"
	insertThreadIndex = "INSERT OR REPLACE INTO %s_index" +
		" (thread_id, user_ids)" +
		" VALUES (?, ?);"
	lookupBlacklisted = "SELECT user_id FROM %s_blacklist" +
		" ORDER BY timestamp DESC;"
	lookupLogsThread = "SELECT user_id, content, timestamp, original FROM %s_messages" +
		" WHERE thread_id = ?" +
		" ORDER BY timestamp ASC, id DESC;"
	lookupLogsUser = "SELECT user_id, content, timestamp, original FROM %s_messages" +
		" WHERE thread_id IN (SELECT thread_id FROM %s_index WHERE user_ids LIKE ?)" +
		" ORDER BY timestamp ASC, id DESC;"
	lookupThreads = "SELECT * FROM %s_index" +
		" ORDER BY timestamp DESC" +
		" LIMIT ?" +
		" OFFSET ?;"
	lookupThreadsUsers = "SELECT * FROM %s_index" +
		" WHERE user_ids LIKE ?" +
		" ORDER BY timestamp DESC" +
		" LIMIT ?" +
		" OFFSET ?;"
)

var db *sql.DB

func initTables(chanID string) error {
	query := fmt.Sprintf(createMessagesTable, chanID)
	stmt, err := db.Prepare(query)
	if err != nil {
		return err
	}
	defer stmt.Close()
	if _, err = stmt.Exec(); err != nil {
		return err
	}

	query = fmt.Sprintf(createThreadIndexTable, chanID)
	stmt, err = db.Prepare(query)
	if err != nil {
		return err
	}
	if _, err = stmt.Exec(); err != nil {
		return err
	}

	query = fmt.Sprintf(createBlacklistTable, chanID)
	stmt, err = db.Prepare(query)
	if err != nil {
		return err
	}
	if _, err = stmt.Exec(); err != nil {
		return err
	}

	return nil
}

func openConnection(dbPath string) error {
	var err error

	db, err = sql.Open("sqlite3", dbPath)
	if err != nil {
		return err
	}
	return nil
}
