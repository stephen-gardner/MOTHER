package main

import (
	"bytes"
	"strconv"
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
		Active      bool
		mom         *Mother           `gorm:"-"`
		convIndex   map[string]string `gorm:"-"`
		directIndex map[string]string `gorm:"-"`
	}
	MessageLog struct {
		gorm.Model
		ConversationID  uint
		SlackID         string
		Msg             string `gorm:"type:text"`
		DirectTimestamp string
		ConvTimestamp   string
		Original        bool
	}
)

func (conv *Conversation) addLog(entry *MessageLog) {
	entry.Msg = conv.mom.subDisplayNames(entry.Msg)
	err := db.
		Model(conv).
		Association("MessageLogs").
		Append(entry).Error
	if err != nil {
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

func (conv *Conversation) mirrorAttachment(file slack.File, msgEntry *MessageLog, isDirect bool) error {
	buff := &bytes.Buffer{}
	threadTimestamp := ""
	if file.Size > conv.mom.config.MaxFileSize {
		msg := conv.mom.getMsg("fileTooLarge", []langVar{
			{"FILE_NAME", file.Name},
			{"MAX_FILE_SIZE", strconv.Itoa(conv.mom.config.MaxFileSize)},
		})
		conv.sendMessageToDM(msg)
		conv.sendMessageToThread(msg)
		return nil
	}
	if err := conv.mom.rtm.GetFile(file.URLPrivateDownload, buff); err != nil {
		return err
	}
	chanID := make([]string, 1)
	if isDirect {
		chanID[0] = conv.mom.config.ChanID
		threadTimestamp = conv.ThreadID
	} else {
		chanID[0] = conv.DirectID
	}
	upload, err := conv.mom.rtm.UploadFile(
		slack.FileUploadParameters{
			Reader:          buff,
			Filetype:        file.Filetype,
			Filename:        file.Name,
			Title:           file.Title,
			Channels:        chanID,
			ThreadTimestamp: threadTimestamp,
		},
	)
	if err != nil {
		return err
	}
	var fileURL string
	if isDirect {
		fileURL = upload.URLPrivate
	} else {
		fileURL = file.URLPrivate
	}
	msg := conv.mom.getMsg("uploadedFile", []langVar{
		{"FILE_URL", fileURL},
	})
	entry := &MessageLog{
		SlackID:         file.User,
		Msg:             msg,
		DirectTimestamp: msgEntry.DirectTimestamp + "a",
		ConvTimestamp:   msgEntry.ConvTimestamp + "a",
		Original:        true,
	}
	conv.addLog(entry)
	return nil
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
	_, _, _, err := conv.mom.rtm.UpdateMessage(
		chanID,
		mirrorTimestamp,
		slack.MsgOptionText(conv.mom.getMsg("msgCopyFmt", []langVar{
			{"SLACK_ID", slackID},
			{"MESSAGE", msg},
		}), false),
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

func (conv *Conversation) init(mom *Mother) {
	conv.Active = true
	conv.mom = mom
	conv.convIndex = make(map[string]string)
	conv.directIndex = make(map[string]string)
	for _, entry := range conv.MessageLogs {
		conv.directIndex[entry.DirectTimestamp] = entry.ConvTimestamp
		conv.convIndex[entry.ConvTimestamp] = entry.DirectTimestamp
	}
}

func (conv *Conversation) setActive(state bool) error {
	var err error
	if conv.Active != state {
		err = db.
			Model(conv).
			UpdateColumn("active", state).Error
		conv.Active = state
	}
	return err
}

func (conv *Conversation) abandon() {
	for i, c := range conv.mom.Conversations {
		if c.ThreadID != conv.ThreadID {
			continue
		}
		conv.mom.Conversations = append(conv.mom.Conversations[:i], conv.mom.Conversations[i+1:]...)
		if err := conv.setActive(false); err != nil {
			conv.mom.log.Println(err)
		}
		break
	}
	if _, _, err := conv.mom.rtm.DeleteMessage(conv.mom.config.ChanID, conv.ThreadID); err != nil {
		// In the worst case, this could result in an ugly situation where channel members are unknowingly sending
		// messages to an inactive thread, but the chances of this many things suddenly going wrong is extremely
		// unlikely
		conv.mom.log.Println(err)
	}
}

func (conv *Conversation) expire() {
	if err := conv.setActive(false); err != nil {
		conv.mom.log.Println(err)
	}
	conv.sendMessageToDM(conv.mom.getMsg("sessionExpiredDirect", nil))
	conv.sendMessageToThread(conv.mom.getMsg("sessionExpiredConv", []langVar{
		{"THREAD_ID", conv.ThreadID},
	}))
}

func (conv *Conversation) update() {
	now := time.Now()
	err := db.
		Model(conv).
		Update("updated_at", now).Error
	if err != nil {
		conv.mom.log.Println(err)
	}
	conv.UpdatedAt = now
	if err = conv.setActive(true); err != nil {
		conv.mom.log.Println(err)
	}
}
