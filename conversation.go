package main

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/nlopes/slack"
)

type (
	logEntry struct {
		userID    string
		msg       string
		timestamp string
		original  bool
	}

	conversation struct {
		mom         *mother
		dmID        string
		threadID    string
		userIDs     []string
		logs        map[string]logEntry
		convIndex   map[string]string
		directIndex map[string]string
		editedLogs  []logEntry
		lastUpdate  int64
	}
)

func newConversation(mom *mother, dmID, threadID string, userIDs []string) *conversation {
	conv := conversation{mom: mom, dmID: dmID, threadID: threadID, userIDs: userIDs}
	conv.logs = make(map[string]logEntry)
	conv.convIndex = make(map[string]string)
	conv.directIndex = make(map[string]string)
	conv.editedLogs = []logEntry{}
	conv.update()
	return &conv
}

func (conv *conversation) hasLog(timestamp string) bool {
	present := timestamp == conv.threadID
	if !present {
		_, present = conv.directIndex[timestamp]
	}
	if !present {
		_, present = conv.convIndex[timestamp]
	}
	return present
}

func (conv *conversation) addLog(directTimestamp, convTimestamp string, log logEntry) {
	if prev, present := conv.logs[directTimestamp]; present {
		conv.editedLogs = append(conv.editedLogs, prev)
	}

	conv.logs[directTimestamp] = log
	conv.directIndex[directTimestamp] = convTimestamp
	conv.convIndex[convTimestamp] = directTimestamp
	conv.update()
}

func (conv *conversation) update() {
	conv.lastUpdate = time.Now().Unix()
}

func (conv *conversation) save(prefix string) error {
	var (
		query string
		stmt  *sql.Stmt
		err   error
	)

	query = fmt.Sprintf(insertThreadIndex, prefix)
	stmt, err = db.Prepare(query)
	if err != nil {
		return err
	}
	defer stmt.Close()
	if _, err = stmt.Exec(conv.threadID, strings.Join(conv.userIDs, ",")); err != nil {
		return err
	}

	query = fmt.Sprintf(insertMessage, prefix)
	stmt, err = db.Prepare(query)
	if err != nil {
		return err
	}
	for key, log := range conv.logs {
		_, err = stmt.Exec(log.userID, conv.threadID, log.msg, log.timestamp, log.original)
		if err != nil {
			return err
		}
		delete(conv.logs, key)
	}
	return nil
}

func loadConversation(mom *mother, threadID string) (*conversation, error) {
	var userIDs string

	query := fmt.Sprintf(findThreadIndex, mom.chanID)
	stmt, err := db.Prepare(query)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	result, err := stmt.Query(threadID)
	if err != nil {
		return nil, err
	}
	defer result.Close()
	if !result.Next() {
		return nil, nil
	}
	err = result.Scan(&userIDs)
	if err != nil {
		return nil, err
	}

	users := strings.Split(userIDs, ",")
	for _, user := range users {
		if mom.hasMember(user) {
			return nil, nil
		}
	}

	params := slack.OpenConversationParameters{ChannelID: "", ReturnIM: true, Users: users}
	channel, _, _, err := mom.rtm.OpenConversation(&params)
	if err != nil {
		return nil, err
	}

	return newConversation(mom, channel.ID, threadID, users), nil
}
