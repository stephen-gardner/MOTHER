package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/nlopes/slack"
)

type (
	threadInfo struct {
		threadID  string
		userIDs   []string
		timestamp string
	}

	mother struct {
		rtm        *slack.RTM
		chanID     string
		members    []string
		convos     map[string]conversation
		online     bool
		lastUpdate int64
	}
)

func newMother(token, name, chanID string) *mother {
	api := slack.New(token,
		slack.OptionDebug(true),
		slack.OptionLog(log.New(os.Stdout, name+": ", log.Lshortfile|log.LstdFlags)),
	)
	rtm := api.NewRTM()
	go rtm.ManageConnection()

	mom := &mother{rtm: rtm, chanID: chanID, online: true, lastUpdate: time.Now().Unix()}
	mom.members = []string{}
	mom.convos = make(map[string]conversation)
	return mom
}

func (mom *mother) hasMember(userID string) bool {
	for _, member := range mom.members {
		if userID == member {
			return true
		}
	}
	return false
}

func (mom *mother) updateMembers() {
	params := slack.GetUsersInConversationParameters{ChannelID: mom.chanID, Cursor: "", Limit: 0}
	members, _, err := mom.rtm.GetUsersInConversation(&params)
	if err != nil {
		log.Println(err)
		return
	}
	mom.members = members
}

func (mom *mother) lookupLogs(id string, isUser bool) ([]logEntry, error) {
	var query string

	if isUser {
		query = fmt.Sprintf(lookupLogsUser, mom.chanID, mom.chanID)
		id = "%" + id + "%"
	} else {
		query = fmt.Sprintf(lookupLogsThread, mom.chanID)
	}

	stmt, err := db.Prepare(query)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	result, err := stmt.Query(id)
	if err != nil {
		return nil, err
	}
	defer result.Close()

	logs := make([]logEntry, 0)
	for result.Next() {
		var entry logEntry

		err = result.Scan(&entry.userID, &entry.msg, &entry.timestamp, &entry.original)
		if err != nil {
			return nil, err
		}
		logs = append(logs, entry)
	}
	return logs, nil
}

func (mom *mother) lookupThreads(userID string, page int) ([]threadInfo, error) {
	var (
		query  string
		stmt   *sql.Stmt
		result *sql.Rows
		err    error
	)

	if userID != "" {
		query = fmt.Sprintf(lookupThreadsUser, mom.chanID)
		stmt, err = db.Prepare(query)
		if err != nil {
			return nil, err
		}
		defer stmt.Close()
		result, err = stmt.Query("%"+userID+"%", 10, 10*(page-1))
	} else {
		query = fmt.Sprintf(lookupThreads, mom.chanID)
		stmt, err = db.Prepare(query)
		if err != nil {
			return nil, err
		}
		defer stmt.Close()
		result, err = stmt.Query(10, 10*(page-1))
	}
	if err != nil {
		return nil, err
	}
	defer result.Close()

	threads := make([]threadInfo, 0)
	for result.Next() {
		var (
			info    threadInfo
			userIDs string
		)

		err = result.Scan(&info.threadID, &userIDs, &info.timestamp)
		if err != nil {
			return nil, err
		}
		info.userIDs = strings.Split(userIDs, ",")
		threads = append(threads, info)
	}
	return threads, nil
}
