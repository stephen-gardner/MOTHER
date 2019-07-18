package main

import (
	"fmt"
	"github.com/nlopes/slack"
	"os"
)

type forwardingParams struct {
	conv            *conversation
	files           []slack.File
	chanID          string
	threadID        string
	userID          string
	directTimestamp string
	convTimestamp   string
}

func forwardAttachment(mom *mother, params forwardingParams) error {
	var attach *slack.File

	for _, file := range params.files {
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
	chanArray[0] = params.chanID
	upload, err := mom.rtm.UploadFile(
		slack.FileUploadParameters{
			File:            attach.Name,
			Filename:        attach.Name,
			Title:           attach.Title,
			Channels:        chanArray,
			ThreadTimestamp: params.threadID,
		},
	)
	if err != nil {
		return err
	}

	err = os.Remove(attach.Name)
	if err != nil {
		mom.log.Println(err)
	}

	entry := logEntry{
		userID:    params.userID,
		msg:       fmt.Sprintf(mom.getMsg("uploadedFile"), upload.URLPrivateDownload),
		timestamp: params.convTimestamp + "a",
		original:  true,
	}
	params.conv.addLog(params.directTimestamp+"a", params.convTimestamp+"a", entry)
	return nil
}

func handleChannelMessageEvent(mom *mother, ev *slack.MessageEvent) {
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

			if len(ev.Files) == 0 {
				return
			}
			params := forwardingParams{
				conv:            conv,
				files:           ev.Files,
				chanID:          conv.dmID,
				threadID:        "",
				userID:          ev.User,
				directTimestamp: directTimestamp,
				convTimestamp:   ev.Timestamp,
			}
			err = forwardAttachment(mom, params)
			if err != nil {
				mom.log.Println(err)
				ref := slack.NewRefToMessage(ev.Channel, ev.Timestamp)
				err := mom.rtm.AddReaction(mom.getMsg("reactFailure"), ref)
				if err != nil {
					mom.log.Println(err)
				}
			}
			return
		}
	}

	if ev.Text != "" && ev.Text[0] == '!' {
		mom.runCommand(ev)
	}
}

func handleDirectMessageEvent(mom *mother, ev *slack.MessageEvent, chanInfo *slack.Channel) {
	rtm := mom.rtm
	for _, userID := range chanInfo.Members {
		if mom.isBlacklisted(userID) {
			out := rtm.NewOutgoingMessage(fmt.Sprintf(mom.getMsg("blacklistedUser"), userID), ev.Channel)
			rtm.SendMessage(out)
			return
		}
		// If conversation contains a channel member, direct message must be a command from a member
		if mom.hasMember(userID) {
			if (ev.User == userID || mom.hasMember(ev.User)) && ev.Text != "" && ev.Text[0] == '!' {
				mom.runCommand(ev)
			} else {
				chanName := mom.getChannelInfo(mom.config.ChanID).Name
				out := rtm.NewOutgoingMessage(fmt.Sprintf(mom.getMsg("inConvChannel"), chanName), ev.Channel)
				rtm.SendMessage(out)
			}
			return
		}
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

	if len(ev.Files) == 0 {
		return
	}
	params := forwardingParams{
		conv:            conv,
		files:           ev.Files,
		chanID:          mom.config.ChanID,
		threadID:        conv.threadID,
		userID:          ev.User,
		directTimestamp: ev.Timestamp,
		convTimestamp:   convTimestamp,
	}
	err = forwardAttachment(mom, params)
	if err != nil {
		mom.log.Println(err)
		ref := slack.NewRefToMessage(ev.Channel, ev.Timestamp)
		err := mom.rtm.AddReaction(mom.getMsg("reactFailure"), ref)
		if err != nil {
			mom.log.Println(err)
		}
	}
}

func handleMessageChangedEvent(mom *mother, ev *slack.MessageEvent, chanInfo *slack.Channel) {
	if conv := mom.findConversation(ev.SubMessage.Timestamp, false); conv != nil {
		conv.updateMessage(
			ev.SubMessage.User,
			ev.SubMessage.Timestamp,
			ev.SubMessage.Text,
			chanInfo.IsIM || chanInfo.IsMpIM,
		)
	}
}

func handleUserTypingEvent(mom *mother, ev *slack.UserTypingEvent) {
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

func handleReactionAddedEvent(mom *mother, ev *slack.ReactionAddedEvent) {
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

func handleReactionRemovedEvent(mom *mother, ev *slack.ReactionRemovedEvent) {
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
