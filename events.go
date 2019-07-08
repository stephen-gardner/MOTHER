package main

import (
	"fmt"
	"log"

	"github.com/nlopes/slack"
)

func (mom *mother) handleChannelMessageEvent(ev *slack.MessageEvent) {
	threadID := ev.ThreadTimestamp
	if threadID != "" {
		if conv := mom.findConversation(threadID, true); conv != nil {
			msg := fmt.Sprintf(msgCopyFmt, ev.User, ev.Text)
			directTimestamp, err := conv.postMessageToDM(msg)
			if err != nil {
				return
			}
			entry := logEntry{
				userID:    ev.User,
				msg:       ev.Text,
				timestamp: ev.Timestamp,
				original:  true,
			}
			conv.addLog(directTimestamp, ev.Timestamp, entry)
			return
		}
	}
}

func (mom *mother) handleDirectMessageEvent(ev *slack.MessageEvent, chanInfo *slack.Channel) {
	member := false
	for _, userID := range chanInfo.Members {
		if mom.hasMember(userID) {
			member = true
		}
	}
	if member {
		if ev.Text[0] == '!' {

		} else {
			chanName := mom.getChannelInfo(mom.chanID).Name
			out := mom.rtm.NewOutgoingMessage(fmt.Sprintf(inConvChannel, chanName), ev.Channel)
			mom.rtm.SendMessage(out)
		}
		return
	}

	var (
		conv *conversation
		err  error
	)

	conv, present := mom.convos[ev.Channel]
	if !present {
		conv, err = mom.startConversation(chanInfo.Members, ev.Channel, true)
		if err != nil {
			log.Println(err)
			mom.rtm.SendMessage(mom.rtm.NewOutgoingMessage(highVolumeError, ev.Channel))
			return
		}
	}

	msg := fmt.Sprintf(msgCopyFmt, ev.User, ev.Text)
	convTimestamp, err := conv.postMessageToThread(msg)
	if err != nil {
		return
	}
	entry := logEntry{
		userID:    ev.User,
		msg:       ev.Text,
		timestamp: convTimestamp,
		original:  true,
	}
	conv.addLog(ev.Timestamp, convTimestamp, entry)
}

func (mom *mother) handleMessageChangedEvent(ev *slack.MessageEvent, chanInfo *slack.Channel) {
	conv := mom.findConversation(ev.SubMessage.Timestamp, false)
	if conv != nil {
		conv.updateMessage(
			ev.SubMessage.User,
			ev.SubMessage.Timestamp,
			ev.SubMessage.Text,
			chanInfo.IsIM || chanInfo.IsMpIM,
		)
	}
}

func (mom *mother) handleUserTypingEvent(ev *slack.UserTypingEvent) {
	if ev.User == mom.rtm.GetInfo().User.ID || mom.hasMember(ev.User) {
		return
	}
	chanInfo := mom.getChannelInfo(ev.Channel)
	if chanInfo == nil {
		return
	}
	if chanInfo.IsIM || chanInfo.IsMpIM {
		mom.rtm.SendMessage(mom.rtm.NewTypingMessage(mom.chanID))
	}
}

func (mom *mother) handleReactionAddedEvent(ev *slack.ReactionAddedEvent) {
	if ev.User == mom.rtm.GetInfo().User.ID {
		return
	}
	chanInfo := mom.getChannelInfo(ev.Item.Channel)
	if chanInfo == nil {
		return
	}
	conv := mom.findConversation(ev.Item.Timestamp, false)
	if conv != nil {
		conv.setReaction(ev.Item.Timestamp, ev.Reaction, chanInfo.IsIM || chanInfo.IsMpIM, false)
	}
}

func (mom *mother) handleReactionRemovedEvent(ev *slack.ReactionRemovedEvent) {
	if ev.User == mom.rtm.GetInfo().User.ID {
		return
	}
	chanInfo := mom.getChannelInfo(ev.Item.Channel)
	if chanInfo == nil {
		return
	}
	conv := mom.findConversation(ev.Item.Timestamp, false)
	if conv != nil {
		conv.setReaction(ev.Item.Timestamp, ev.Reaction, chanInfo.IsIM || chanInfo.IsMpIM, true)
	}
}
