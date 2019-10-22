package main

import (
	"sort"

	"github.com/nlopes/slack"
)

func handleMemberJoinedChannelEvent(mom *Mother, ev *slack.MemberJoinedChannelEvent) {
	if ev.Channel != mom.config.ChanID {
		return
	}
	if mom.getUserInfo(mom.rtm.GetInfo().User.ID).IsAdmin {
		if !mom.isInvited(ev.User) {
			if err := mom.rtm.KickUserFromConversation(ev.Channel, ev.User); err != nil {
				mom.log.Println(err)
				return
			}
		} else {
			mom.removeInvitation(ev.User)
		}
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
}

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
