package main

import (
	"fmt"
	"time"

	"github.com/jinzhu/gorm"
	"github.com/nlopes/slack"
)

type (
	Conversation struct {
		gorm.Model
		MotherID    uint
		SlackIDs    string
		DirectID    string
		ThreadID    string
		MessageLogs []MessageLog
		mom         *Mother           `gorm:"-"`
		convIndex   map[string]string `gorm:"-"`
		directIndex map[string]string `gorm:"-"`
		active      bool              `gorm:"-"`
	}

	MessageLog struct {
		gorm.Model
		ConversationID  uint
		SlackID         string
		Msg             string
		DirectTimestamp string
		ConvTimestamp   string
		Original        bool
	}
)

func (conv *Conversation) init(mom *Mother) {
	conv.mom = mom
	conv.convIndex = make(map[string]string)
	conv.directIndex = make(map[string]string)
	conv.active = true
	for _, entry := range conv.MessageLogs {
		conv.directIndex[entry.DirectTimestamp] = entry.ConvTimestamp
		conv.convIndex[entry.ConvTimestamp] = entry.DirectTimestamp
	}
}

func (conv *Conversation) addLog(entry *MessageLog) {
	if err := db.Model(conv).Association("MessageLogs").Append(entry).Error; err != nil {
		conv.mom.log.Println(err)
	}
	conv.directIndex[entry.DirectTimestamp] = entry.ConvTimestamp
	conv.convIndex[entry.ConvTimestamp] = entry.DirectTimestamp
	conv.update()
}

func (conv *Conversation) hasLog(timestamp string) bool {
	present := timestamp == conv.ThreadID
	if !present {
		_, present = conv.directIndex[timestamp]
	}
	if !present {
		_, present = conv.convIndex[timestamp]
	}
	return present
}

func (conv *Conversation) postMessageToThread(msg string) (string, error) {
	return conv.mom.postMessage(conv.mom.config.ChanID, conv.ThreadID, msg)
}

func (conv *Conversation) postMessageToDM(msg string) (string, error) {
	return conv.mom.postMessage(conv.DirectID, "", msg)
}

func (conv *Conversation) sendMessageToThread(msg string) {
	conv.mom.rtm.SendMessage(
		conv.mom.rtm.NewOutgoingMessage(
			msg,
			conv.mom.config.ChanID,
			slack.RTMsgOptionTS(conv.ThreadID),
		),
	)
}

func (conv *Conversation) sendMessageToDM(msg string) {
	conv.mom.rtm.SendMessage(conv.mom.rtm.NewOutgoingMessage(msg, conv.DirectID))
}

func (conv *Conversation) mirrorEdit(slackID, timestamp, msg string, isDirect bool) {
	var chanID, convTimestamp, directTimestamp, mirrorTimestamp string
	if isDirect {
		if _, present := conv.directIndex[timestamp]; !present {
			return
		}
		convTimestamp = conv.directIndex[timestamp]
		directTimestamp = timestamp
		mirrorTimestamp = convTimestamp
		chanID = conv.mom.config.ChanID
	} else {
		if _, present := conv.convIndex[timestamp]; !present {
			return
		}
		convTimestamp = timestamp
		directTimestamp = conv.convIndex[timestamp]
		mirrorTimestamp = directTimestamp
		chanID = conv.DirectID
	}
	tagged := fmt.Sprintf(conv.mom.getMsg("msgCopyFmt"), slackID, msg)
	_, _, _, err := conv.mom.rtm.UpdateMessage(
		chanID,
		mirrorTimestamp,
		slack.MsgOptionText(tagged, false),
	)
	if err != nil {
		conv.mom.log.Println(err)
		return
	}
	entry := &MessageLog{
		ConversationID:  conv.ID,
		SlackID:         slackID,
		Msg:             msg,
		DirectTimestamp: directTimestamp,
		ConvTimestamp:   convTimestamp,
		Original:        false,
	}
	conv.addLog(entry)
}

func (conv *Conversation) mirrorReaction(timestamp, emoji string, isDirect, removed bool) {
	var targetRef slack.ItemRef

	if isDirect {
		if _, present := conv.directIndex[timestamp]; !present {
			return
		}
		targetRef = slack.NewRefToMessage(conv.mom.config.ChanID, conv.directIndex[timestamp])
	} else {
		if _, present := conv.convIndex[timestamp]; !present {
			return
		}
		targetRef = slack.NewRefToMessage(conv.DirectID, conv.convIndex[timestamp])
	}

	if removed {
		_ = conv.mom.rtm.RemoveReaction(emoji, targetRef)
	} else {
		_ = conv.mom.rtm.AddReaction(emoji, targetRef)
	}
	conv.update()
}

func (conv *Conversation) expire() {
	conv.active = false
	conv.sendMessageToDM(conv.mom.getMsg("sessionExpiredDirect"))
	conv.sendMessageToThread(fmt.Sprintf(conv.mom.getMsg("sessionExpiredConv"), conv.ThreadID))
}

func (conv *Conversation) update() {
	if err := db.Model(conv.mom).Update("updated_at", time.Now()).Error; err != nil {
		conv.mom.log.Println(err)
	}
}
