package main

import (
	"database/sql"
	"fmt"
	"log"
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

func (conv *conversation) postMessageToThread(msg string) (string, error) {
	return conv.mom.postMessage(conv.mom.chanID, msg, conv.threadID)
}

func (conv *conversation) postMessageToDM(msg string) (string, error) {
	return conv.mom.postMessage(conv.dmID, msg, "")
}

func (conv *conversation) sendMessageToThread(msg string) {
	out := conv.mom.rtm.NewOutgoingMessage(
		msg,
		conv.mom.chanID,
		slack.RTMsgOptionTS(conv.threadID),
	)
	conv.mom.rtm.SendMessage(out)
}

func (conv *conversation) sendMessageToDM(msg string) {
	conv.mom.rtm.SendMessage(conv.mom.rtm.NewOutgoingMessage(msg, conv.dmID))
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

	if removed {
		_ = conv.mom.rtm.RemoveReaction(emoji, msgRef)
	} else {
		_ = conv.mom.rtm.AddReaction(emoji, msgRef)
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
	_, _, _, err := conv.mom.rtm.UpdateMessage(
		chanID,
		timestamp,
		slack.MsgOptionText(tagged, false),
	)
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
	for _, entry := range conv.editedLogs {
		_, err = stmt.Exec(entry.userID, conv.threadID, entry.msg, entry.timestamp, entry.original)
		if err != nil {
			return err
		}
		conv.editedLogs = conv.editedLogs[1:]
	}
	return nil
}

func (conv *conversation) update() {
	conv.lastUpdate = time.Now().Unix()
}
