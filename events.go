package main

import (
	"fmt"
	"sort"
	"time"

	"github.com/nlopes/slack"
)

type (
	blacklistEvent struct {
		Type    string
		SlackID string
	}

	scrubEvent struct {
		Type string
	}
)

func handleChannelMessageEvent(mom *Mother, ev *slack.MessageEvent, sender *slack.User) {
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
		mom.runCommand(ev, sender, true)
	}
}

func handleDirectMessageEvent(mom *Mother, ev *slack.MessageEvent, sender *slack.User, chanInfo *slack.Channel) {
	hasMember := false
	// Cannot do anything with blacklisted user present
	for _, userID := range chanInfo.Members {
		if mom.isBlacklisted(userID) {
			msg := fmt.Sprintf(mom.getMsg("blacklistedUser"), userID)
			mom.rtm.SendMessage(mom.rtm.NewOutgoingMessage(msg, ev.Channel))
			return
		}
		if mom.hasMember(userID) {
			hasMember = true
		}
	}
	// Accept commands from channel members or workspace admins
	if ev.Text != "" && ev.Text[0] == '!' && (sender.IsAdmin || mom.hasMember(sender.ID)) {
		mom.runCommand(ev, sender, false)
		return
	}
	// Conversations cannot be held if a channel member is present
	if hasMember {
		memberChanInfo, err := mom.getChannelInfo(mom.config.ChanID)
		if err != nil {
			mom.log.Println(err)
		}
		msg := fmt.Sprintf(mom.getMsg("inConvChannel"), memberChanInfo.Name)
		mom.rtm.SendMessage(mom.rtm.NewOutgoingMessage(msg, ev.Channel))
		return
	}

	var convTimestamp string
	var err error
	conv := mom.findConversationByChannel(ev.Channel)
	if conv == nil {
		conv, err = mom.
			newConversation().
			postNewThread(ev.Channel, chanInfo.Members).
			create()
		if err != nil {
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

// Forward edits of active conversation's messages between direct messages and conversation threads
func handleMessageChangedEvent(mom *Mother, ev *slack.MessageEvent, chanInfo *slack.Channel) {
	if conv := mom.findConversationByTimestamp(ev.SubMessage.Timestamp, false); conv != nil {
		conv.mirrorEdit(
			ev.SubMessage.User,
			ev.SubMessage.Timestamp,
			ev.SubMessage.Text,
			chanInfo.IsIM || chanInfo.IsMpIM,
		)
	}
}

func handleMessageEvent(mom *Mother, ev *slack.MessageEvent) {
	if ev.SubType == "message_replied" {
		return // Thread update events
	}
	var sender *slack.User
	var err error
	edit := ev.SubType == "message_changed"
	if edit {
		sender, err = mom.getUserInfo(ev.SubMessage.User)
	} else {
		sender, err = mom.getUserInfo(ev.User)
	}
	if err != nil {
		mom.log.Println(err)
		return
	}
	if sender.IsBot || mom.isBlacklisted(sender.ID) {
		return
	}
	chanInfo, err := mom.getChannelInfo(ev.Channel)
	if err != nil {
		mom.log.Println(err)
		return
	}
	if edit {
		handleMessageChangedEvent(mom, ev, chanInfo)
	} else if ev.Channel == mom.config.ChanID {
		handleChannelMessageEvent(mom, ev, sender)
	} else if chanInfo.IsIM || chanInfo.IsMpIM {
		handleDirectMessageEvent(mom, ev, sender, chanInfo)
	}
}

// Leave random public channels that bot gets invited into
func handleChannelJoinedEvent(mom *Mother, ev *slack.ChannelJoinedEvent) {
	if ev.Channel.ID == mom.config.ChanID {
		return
	}
	if _, err := mom.rtm.LeaveChannel(ev.Channel.ID); err != nil {
		mom.log.Println(err)
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

func handleEvents(mom *Mother) {
	for msg := range mom.events {
		switch ev := msg.Data.(type) {
		case *blacklistEvent:
			mom.blacklistUser(ev.SlackID)

		case *scrubEvent:
			mom.reapConversations()
			mom.pruneUsers()
			mom.spoofAvailability()

		case *slack.ChannelJoinedEvent:
			handleChannelJoinedEvent(mom, ev)

		case *slack.ConnectionErrorEvent:
			mom.log.Printf("Connection error (%d attempts): %s\n", ev.Attempt, ev.Error())

		case *slack.ConnectedEvent:
			mom.connectedAt = time.Now()
			mom.log.Printf("Connected (#%d)...\n", ev.ConnectionCount+1)

		case *slack.DisconnectedEvent:
			mom.log.Printf("Disconnected (Intentional: %v, Reload: %v)...\n", ev.Intentional, mom.reload)
			if ev.Intentional {
				// We need the main thread to count this bot in the event of a reload to prevent premature shutdown
				// The key will be overwritten anyway
				if !mom.reload {
					mothers.Delete(mom.Name)
				}
				close(mom.shutdown)
				return
			}

		case *slack.GroupJoinedEvent:
			handleGroupJoinedEvent(mom, ev)

		case *slack.InvalidAuthEvent:
			mom.log.Println("Invalid credentials")
			mothers.Delete(mom)
			close(mom.shutdown)
			return

		case *slack.MemberJoinedChannelEvent:
			handleMemberJoinedChannelEvent(mom, ev)

		case *slack.MemberLeftChannelEvent:
			handleMemberLeftChannelEvent(mom, ev)

		case *slack.MessageEvent:
			handleMessageEvent(mom, ev)

		case *slack.RateLimitedError:
			mom.log.Printf("Hitting RTM rate limit; sleeping for %d seconds\n", ev.RetryAfter)
			time.Sleep(ev.RetryAfter * time.Second)

		case *slack.ReactionAddedEvent:
			handleReactionAddedEvent(mom, ev)

		case *slack.ReactionRemovedEvent:
			handleReactionRemovedEvent(mom, ev)

		case *slack.RTMError:
			mom.log.Println("Error:", ev.Error())

		case *slack.UserTypingEvent:
			handleUserTypingEvent(mom, ev)

		default:
			// Ignore other events..
		}
	}
}
