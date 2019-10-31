package main

import (
	"fmt"
	"sort"

	"github.com/nlopes/slack"
)

// Leave random public channels that bot gets invited into
func handleChannelJoinedEvent(mom *Mother, ev *slack.ChannelJoinedEvent) {
	if ev.Channel.ID == mom.config.ChanID {
		return
	}
	if _, err := mom.rtm.LeaveChannel(ev.Channel.ID); err != nil {
		mom.log.Println(err)
	}
}

func handleChannelMessageEvent(mom *Mother, ev *slack.MessageEvent) {
	userInfo, err := mom.getUserInfo(ev.User)
	if err != nil {
		mom.log.Println(err)
		return
	}
	if userInfo.IsBot {
		return
	}
	var conv *Conversation
	if ev.ThreadTimestamp != "" {
		conv = mom.findConversationByTimestamp(ev.ThreadTimestamp, true)
	}
	if conv != nil {
		msg := fmt.Sprintf(mom.getMsg("msgCopyFmt"), ev.User, ev.Text)
		directTimestamp, err := conv.postMessageToDM(msg)
		if err != nil {
			return
		}
		entry := &MessageLog{
			SlackID:         ev.User,
			Msg:             ev.Text,
			DirectTimestamp: directTimestamp,
			ConvTimestamp:   ev.Timestamp,
			Original:        true,
		}
		conv.addLog(entry)

		for _, attach := range ev.Files {
			if attach.URLPrivateDownload == "" {
				continue
			}
			if err := conv.mirrorAttachment(attach, entry, false); err != nil {
				mom.log.Println(err)
			}
		}
		return
	}
	if ev.Text != "" && ev.Text[0] == '!' {
		mom.runCommand(ev, true)
	}
}

func handleDirectMessageEvent(mom *Mother, ev *slack.MessageEvent, chanInfo *slack.Channel) {
	isCommand := false
	executeCommand := false
	userInfo, err := mom.getUserInfo(ev.User)
	if err != nil {
		mom.log.Println(err)
		return
	}
	if userInfo.IsBot {
		return
	}
	if ev.Text != "" && ev.Text[0] == '!' {
		isCommand = true
	}
	for _, userID := range chanInfo.Members {
		// Cannot do anything with blacklisted user present
		if mom.isBlacklisted(userID) {
			msg := fmt.Sprintf(mom.getMsg("blacklistedUser"), userID)
			mom.rtm.SendMessage(mom.rtm.NewOutgoingMessage(msg, ev.Channel))
			return
		}
		// If conversation contains a channel executeCommand, direct message must be a executeCommand from a executeCommand
		if !executeCommand && ((isCommand && userInfo.IsAdmin) || mom.hasMember(userID)) {
			executeCommand = true
		}
	}
	if executeCommand {
		if isCommand && (userInfo.IsAdmin || mom.hasMember(ev.User)) {
			mom.runCommand(ev, false)
		} else {
			chanInfo, err := mom.getChannelInfo(mom.config.ChanID)
			if err != nil {
				mom.log.Println(err)
			}
			msg := fmt.Sprintf(mom.getMsg("inConvChannel"), chanInfo.Name)
			mom.rtm.SendMessage(mom.rtm.NewOutgoingMessage(msg, ev.Channel))
		}
		return
	}

	var convTimestamp string
	conv := mom.findConversationByChannel(ev.Channel)
	if conv == nil {
		if conv, err = mom.createConversation(ev.Channel, chanInfo.Members, true); err != nil {
			mom.log.Println(err)
			return
		}
	}
	msg := fmt.Sprintf(mom.getMsg("msgCopyFmt"), ev.User, ev.Text)
	if convTimestamp, err = conv.postMessageToThread(msg); err != nil {
		return
	}
	entry := &MessageLog{
		SlackID:         ev.User,
		Msg:             ev.Text,
		DirectTimestamp: ev.Timestamp,
		ConvTimestamp:   convTimestamp,
		Original:        true,
	}
	conv.addLog(entry)

	for _, attach := range ev.Files {
		if attach.URLPrivateDownload == "" {
			continue
		}
		if err := conv.mirrorAttachment(attach, entry, true); err != nil {
			mom.log.Println(err)
		}
	}
}

// Leaves random private channels that bot gets invited into
func handleGroupJoinedEvent(mom *Mother, ev *slack.GroupJoinedEvent) {
	if ev.Channel.ID == mom.config.ChanID || ev.Channel.IsMpIM {
		return
	}
	if err := mom.rtm.LeaveGroup(ev.Channel.ID); err != nil {
		mom.log.Println(err)
	}
}

// Keep track of new members
func handleMemberJoinedChannelEvent(mom *Mother, ev *slack.MemberJoinedChannelEvent) {
	if ev.Channel != mom.config.ChanID {
		return
	}
	// Prevent users from being accidentally invited to the member channel; requires admin privileges
	if userInfo, err := mom.getUserInfo(mom.rtm.GetInfo().User.ID); err == nil {
		if userInfo.IsAdmin {
			if !mom.isInvited(ev.User) {
				if err := mom.rtm.KickUserFromConversation(ev.Channel, ev.User); err != nil {
					mom.log.Println(err)
					return
				}
			} else {
				mom.removeInvitation(ev.User)
			}
		}
	} else {
		mom.log.Println(err)
		return
	}
	chanInfo, err := mom.getChannelInfo(ev.Channel)
	if err != nil {
		mom.log.Println(err)
		return
	}
	if !mom.hasMember(ev.User) {
		chanInfo.Members = append(chanInfo.Members, ev.User)
		sort.Strings(chanInfo.Members)
	}
	mom.deactivateConversations(ev.User)
}

// Update member list
func handleMemberLeftChannelEvent(mom *Mother, ev *slack.MemberLeftChannelEvent) {
	if ev.Channel != mom.config.ChanID {
		return
	}
	chanInfo, err := mom.getChannelInfo(ev.Channel)
	if err != nil {
		mom.log.Println(err)
		return
	}
	for i, member := range chanInfo.Members {
		if member == ev.User {
			chanInfo.Members = append(chanInfo.Members[:i], chanInfo.Members[i+1:]...)
			return
		}
	}
}

// Forward edits of active conversation's messages between direct messages and conversation threads
func handleMessageChangedEvent(mom *Mother, ev *slack.MessageEvent, chanInfo *slack.Channel) {
	if conv := mom.findConversationByTimestamp(ev.SubMessage.Timestamp, false); conv != nil {
		conv.mirrorEdit(ev.SubMessage.User, ev.SubMessage.Timestamp, ev.SubMessage.Text, chanInfo.IsIM || chanInfo.IsMpIM)
	}
}

// Forward emoji add between direct message and conversation threads
func handleReactionAddedEvent(mom *Mother, ev *slack.ReactionAddedEvent) {
	if mom.isBlacklisted(ev.User) {
		return
	}
	if conv := mom.findConversationByTimestamp(ev.Item.Timestamp, false); conv != nil {
		chanInfo, err := mom.getChannelInfo(ev.Item.Channel)
		if err != nil {
			mom.log.Println(err)
			return
		}
		conv.mirrorReaction(ev.Item.Timestamp, ev.Reaction, chanInfo.IsIM || chanInfo.IsMpIM, false)
	}
}

// Forward emoji removals between direct messages and conversation threads
func handleReactionRemovedEvent(mom *Mother, ev *slack.ReactionRemovedEvent) {
	if mom.isBlacklisted(ev.User) {
		return
	}
	if conv := mom.findConversationByTimestamp(ev.Item.Timestamp, false); conv != nil {
		chanInfo, err := mom.getChannelInfo(ev.Item.Channel)
		if err != nil {
			mom.log.Println(err)
			return
		}
		conv.mirrorReaction(ev.Item.Timestamp, ev.Reaction, chanInfo.IsIM || chanInfo.IsMpIM, true)
	}
}

// Forward typing events from direct messages to member channel
func handleUserTypingEvent(mom *Mother, ev *slack.UserTypingEvent) {
	if mom.hasMember(ev.User) || mom.isBlacklisted(ev.User) {
		return
	}
	chanInfo, err := mom.getChannelInfo(ev.Channel)
	if err != nil {
		mom.log.Println(err)
		return
	}
	if chanInfo.IsIM || chanInfo.IsMpIM {
		mom.rtm.SendMessage(mom.rtm.NewTypingMessage(mom.config.ChanID))
	}
}
