package main

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/nlopes/slack"
)

type cmdParams struct {
	mom      *mother
	chanID   string
	threadID string
	userID   string
	args     []string
}

var commands = map[string]func(params cmdParams) bool{
	"blacklist": cmdBlacklist,
	"contact":   cmdContact,
	"resume":    cmdResume,
}

func getTaggedUserID(tag string) (string, error) {
	rgx, err := regexp.Compile("<@(.*?)>")
	if err != nil {
		return "", err
	}
	res := rgx.FindStringSubmatch(tag)
	if res == nil {
		return "", errors.New("no tagged user")
	}
	return res[1], nil
}

func cmdBlacklist(params cmdParams) bool {
	mom := params.mom
	if len(params.args) == 0 {
		var sb strings.Builder

		blacklist := mom.blacklist
		sort.Strings(blacklist)
		first := true
		for _, id := range blacklist {
			if first {
				first = false
			} else {
				sb.WriteString(", ")
			}
			sb.WriteString(fmt.Sprintf("<@%s>", id))
		}
		msg := fmt.Sprintf(mom.getMsg("listBlacklisted"), sb.String())
		out := mom.rtm.NewOutgoingMessage(msg, params.chanID, slack.RTMsgOptionTS(params.threadID))
		mom.rtm.SendMessage(out)
		return true
	}

	rm := false
	if params.args[0] == "rm" {
		if len(params.args) < 2 {
			return false
		}
		rm = true
		params.args = params.args[1:]
	}

	users := make([]string, 0)
	for _, tag := range params.args {
		ID, err := getTaggedUserID(tag)
		if err != nil || mom.hasMember(ID) {
			return false
		}
		users = append(users, ID)
	}

	for _, id := range users {
		if rm {
			if !mom.removeBlacklistedUser(id) {
				return false
			}
		} else if !mom.blacklistUser(id) {
			return false
		}
	}
	return true
}

func cmdContact(params cmdParams) bool {
	mom := params.mom
	if len(params.args) == 0 {
		return false
	}
	users := make([]string, 0)
	for _, tag := range params.args {
		ID, err := getTaggedUserID(tag)
		if err != nil || mom.hasMember(ID) || mom.isBlacklisted(ID) {
			return false
		}
		users = append(users, ID)
	}

	var (
		dm  *slack.Channel
		err error
	)
	if conv := mom.findConversationByUsers(users); conv == nil {
		params := slack.OpenConversationParameters{
			ChannelID: "",
			ReturnIM:  true,
			Users:     users,
		}
		dm, _, _, err = mom.rtm.OpenConversation(&params)
		if err != nil {
			return false
		}
		_, err = mom.startConversation(users, dm.ID, true)
	} else {
		_, err = mom.startConversation(users, conv.dmID, false)
	}
	if err != nil {
		mom.log.Println(err)
		out := mom.rtm.NewOutgoingMessage(
			mom.getMsg("highVolumeError"),
			params.chanID,
			slack.RTMsgOptionTS(params.threadID),
		)
		mom.rtm.SendMessage(out)
		return false
	}
	return true
}

func cmdResume(params cmdParams) bool {
	mom := params.mom
	if len(params.args) == 0 {
		return false
	}

	var conv *conversation

	if len(params.args) == 1 && strings.ContainsRune(params.args[0], '.') {
		conv = mom.findConversation(params.args[0], true)
	} else {
		users := make([]string, 0)
		for _, tag := range params.args {
			ID, err := getTaggedUserID(tag)
			if err != nil || mom.hasMember(ID) || mom.isBlacklisted(ID) {
				return false
			}
			users = append(users, ID)
		}
		conv = mom.findConversationByUsers(users)

		if conv == nil {
			threads, err := mom.lookupThreads(&users, 1)
			if err == nil && len(threads) > 0 {
				conv, err = mom.loadConversation(threads[0].threadID)
			}
			if err != nil {
				mom.log.Println(err)
			}
		}
	}

	if conv == nil {
		return false
	}

	if _, err := mom.startConversation(conv.userIDs, conv.dmID, false); err != nil {
		mom.log.Println(err)
		out := mom.rtm.NewOutgoingMessage(
			mom.getMsg("highVolumeError"),
			params.chanID,
			slack.RTMsgOptionTS(params.threadID),
		)
		mom.rtm.SendMessage(out)
		return false
	}
	return true
}
