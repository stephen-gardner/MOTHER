package main

import (
	"fmt"
	"github.com/nlopes/slack"
	"os"
)

func (mom *mother) forwardAttachment(attachments []slack.File, chanID, threadID string) error {
	var attach *slack.File

	for _, file := range attachments {
		if file.URLPrivateDownload != "" {
			attach = &file
			break
		}
	}
	if attach == nil {
		return nil
	}

	file, err := os.Create(attach.Name)
	if err != nil {
		return err
	}
	err = mom.rtm.GetFile(attach.URLPrivateDownload, file)
	_ = file.Close()

	chanArray := make([]string, 1)
	chanArray[0] = chanID
	_, err = mom.rtm.UploadFile(slack.FileUploadParameters{
		File:            attach.Name,
		Filename:        attach.Name,
		Title:           attach.Title,
		Channels:        chanArray,
		ThreadTimestamp: threadID,
	})
	if err != nil {
		return err
	}

	err = os.Remove(attach.Name)
	if err != nil {
		mom.log.Println(err)
	}

	return nil
}

func (mom *mother) handleChannelMessageEvent(ev *slack.MessageEvent) {
	threadID := ev.ThreadTimestamp
	if threadID != "" {
		if conv := mom.findConversation(threadID, true); conv != nil {
			msg := fmt.Sprintf(mom.getMsg("msgCopyFmt"), ev.User, ev.Text)
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

			if len(ev.Files) > 0 {
				err := mom.forwardAttachment(ev.Files, conv.dmID, "")
				if err != nil {
					mom.log.Println(err)
					ref := slack.NewRefToMessage(ev.Channel, ev.Timestamp)
					err := mom.rtm.AddReaction(mom.getMsg("reactFailure"), ref)
					if err != nil {
						mom.log.Println(err)
					}
				}
			}
			return
		}
	}

	if ev.Text != "" && ev.Text[0] == '!' {
		mom.runCommand(ev)
	}
}

func (mom *mother) handleDirectMessageEvent(ev *slack.MessageEvent, chanInfo *slack.Channel) {
	rtm := mom.rtm
	member := false
	for _, userID := range chanInfo.Members {
		if userID == mom.rtm.GetInfo().User.ID {
			continue
		}
		if mom.isBlacklisted(userID) {
			rtm.SendMessage(rtm.NewOutgoingMessage(fmt.Sprintf(mom.getMsg("blacklistedUser"), userID), ev.Channel))
			return
		}
		if mom.hasMember(userID) {
			member = true
		}
	}
	if member {
		if mom.hasMember(ev.User) && ev.Text != "" && ev.Text[0] == '!' {
			mom.runCommand(ev)
		} else {
			chanName := mom.getChannelInfo(mom.config.ChanID).Name
			rtm.SendMessage(rtm.NewOutgoingMessage(fmt.Sprintf(mom.getMsg("inConvChannel"), chanName), ev.Channel))
		}
		return
	}

	var (
		conv          *conversation
		convTimestamp string
		err           error
	)

	conv, present := mom.convos[ev.Channel]
	if !present {
		conv, err = mom.startConversation(chanInfo.Members, ev.Channel, true)
		if err != nil {
			mom.log.Println(err)
			rtm.SendMessage(rtm.NewOutgoingMessage(mom.getMsg("highVolumeError"), ev.Channel))
			return
		}
	}

	msg := fmt.Sprintf(mom.getMsg("msgCopyFmt"), ev.User, ev.Text)
	convTimestamp, err = conv.postMessageToThread(msg)
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

	if len(ev.Files) > 0 {
		err := mom.forwardAttachment(ev.Files, mom.config.ChanID, conv.threadID)
		if err != nil {
			mom.log.Println(err)
			ref := slack.NewRefToMessage(ev.Channel, ev.Timestamp)
			err := mom.rtm.AddReaction(mom.getMsg("reactFailure"), ref)
			if err != nil {
				mom.log.Println(err)
			}
		}
	}
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
	if mom.hasMember(ev.User) || mom.isBlacklisted(ev.User) {
		return
	}
	chanInfo := mom.getChannelInfo(ev.Channel)
	if chanInfo == nil {
		return
	}
	if chanInfo.IsIM || chanInfo.IsMpIM {
		mom.rtm.SendMessage(mom.rtm.NewTypingMessage(mom.config.ChanID))
	}
}

func (mom *mother) handleReactionAddedEvent(ev *slack.ReactionAddedEvent) {
	if mom.isBlacklisted(ev.User) {
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
	if mom.isBlacklisted(ev.User) {
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
