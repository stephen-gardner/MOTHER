package main

import (
	"database/sql"
	"fmt"
	"log"
	"sort"
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
	sort.Strings(userIDs)
	conv := conversation{
		mom:      mom,
		dmID:     dmID,
		threadID: threadID,
		userIDs:  userIDs,
	}
	conv.logs = make(map[string]logEntry)
	conv.convIndex = make(map[string]string)
	conv.directIndex = make(map[string]string)
	conv.editedLogs = make([]logEntry, 0)
	conv.update()
	return &conv
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

func (conv *conversation) sendMessageToThread(msg string) {
	rtm := conv.mom.rtm
	out := rtm.NewOutgoingMessage(msg, conv.mom.chanID, slack.RTMsgOptionTS(conv.threadID))
	rtm.SendMessage(out)
}

func (conv *conversation) sendMessageToDM(msg string) {
	rtm := conv.mom.rtm
	out := rtm.NewOutgoingMessage(msg, conv.dmID)
	rtm.SendMessage(out)
}

func (conv *conversation) sendExpirationNotice() {
	conv.sendMessageToDM(sessionExpiredDirect)
	conv.sendMessageToThread(fmt.Sprintf(sessionExpiredConv, conv.threadID))
}

func (conv *conversation) setReaction(timestamp, emoji string, isDirect, removed bool) {
	var msgRef slack.ItemRef

	if isDirect {
		if _, present := conv.directIndex[timestamp]; !present {
			return
		}
		msgRef = slack.NewRefToMessage(conv.mom.chanID, conv.directIndex[timestamp])
	} else {
		if _, present := conv.convIndex[timestamp]; !present {
			return
		}
		msgRef = slack.NewRefToMessage(conv.dmID, conv.convIndex[timestamp])
	}

	var err error

	if removed {
		err = conv.mom.rtm.RemoveReaction(emoji, msgRef)
	} else {
		err = conv.mom.rtm.AddReaction(emoji, msgRef)
	}
	if err != nil {
		log.Println(err)
	}

	conv.update()
}

func (conv *conversation) updateMessage(userID, timestamp, msg string, isDirect bool) {
	var chanID, convTimestamp, directTimestamp string

	if isDirect {
		if _, present := conv.directIndex[timestamp]; !present {
			return
		}
		convTimestamp = conv.directIndex[timestamp]
		directTimestamp = timestamp
		timestamp = convTimestamp
		chanID = conv.mom.chanID
	} else {
		if _, present := conv.convIndex[timestamp]; !present {
			return
		}
		convTimestamp = timestamp
		directTimestamp = conv.convIndex[timestamp]
		timestamp = directTimestamp
		chanID = conv.dmID
	}

	tagged := fmt.Sprintf(msgCopyFmt, userID, msg)
	_, _, _, err := conv.mom.rtm.UpdateMessage(chanID, timestamp, slack.MsgOptionText(tagged, false))
	if err != nil {
		log.Println(err)
		return
	}
	entry := logEntry{
		userID:    userID,
		msg:       msg,
		timestamp: convTimestamp,
		original:  false,
	}
	conv.addLog(directTimestamp, convTimestamp, entry)
}

func (conv *conversation) resume() {
	conv.sendMessageToThread(sessionResumeConv)
	conv.sendMessageToDM(sessionResumeDirect)
	conv.mom.addConversation(conv)
}

func (conv *conversation) save() error {
	var (
		query string
		stmt  *sql.Stmt
		err   error
	)

	query = fmt.Sprintf(insertThreadIndex, conv.mom.chanID)
	stmt, err = db.Prepare(query)
	if err != nil {
		return err
	}
	defer stmt.Close()
	if _, err = stmt.Exec(conv.threadID, strings.Join(conv.userIDs, ",")); err != nil {
		return err
	}

	query = fmt.Sprintf(insertMessage, conv.mom.chanID)
	stmt, err = db.Prepare(query)
	if err != nil {
		return err
	}
	for key, entry := range conv.logs {
		_, err = stmt.Exec(entry.userID, conv.threadID, entry.msg, entry.timestamp, entry.original)
		if err != nil {
			return err
		}
		delete(conv.logs, key)
	}
	return nil
}

func (conv *conversation) update() {
	conv.lastUpdate = time.Now().Unix()
}
