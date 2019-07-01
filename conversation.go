package main

import "time"

type (
	logEntry struct {
		userID    string
		msg       string
		timestamp string
		original  bool
	}

	conversation struct {
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

func newConversation(dmID string, threadID string, userIDs []string) *conversation {
	conv := conversation{dmID: dmID, threadID: threadID, userIDs: userIDs}

	conv.logs = make(map[string]logEntry)
	conv.convIndex = make(map[string]string)
	conv.directIndex = make(map[string]string)
	conv.editedLogs = []logEntry{}
	conv.update()
	return &conv;
}

func (conv *conversation) hasLog(timestamp string) (present bool) {
	present = timestamp == conv.threadID

	if !present {
		_, present = conv.directIndex[timestamp]
	}

	if !present {
		_, present = conv.convIndex[timestamp]
	}

	return
}

func (conv *conversation) addLog(directTimestamp string, convTimestamp string, log logEntry) {
	prev, present := conv.logs[directTimestamp]

	if present {
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
