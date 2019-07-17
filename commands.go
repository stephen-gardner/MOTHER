package main

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/nlopes/slack"
)

var commands = map[string]func(*mother, string, string, string, []string) bool{
	"blacklist": cmdBlacklist,
	"contact":   cmdContact,
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

func cmdBlacklist(mom *mother, chanID, threadID, _ string, args []string) bool {
	if len(args) == 0 {
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
		mom.rtm.SendMessage(mom.rtm.NewOutgoingMessage(msg, chanID, slack.RTMsgOptionTS(threadID)))
		return true
	}

	rm := false
	if args[0] == "rm" {
		if len(args) < 2 {
			return false
		}
		rm = true
		args = args[1:]
	}

	users := make([]string, 0)
	for _, tag := range args {
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

func cmdContact(mom *mother, chanID, threadID, _ string, args []string) bool {
	if len(args) == 0 {
		return false
	}
	users := make([]string, 0)
	for _, tag := range args {
		ID, err := getTaggedUserID(tag)
		if err != nil || mom.hasMember(ID) || mom.isBlacklisted(ID) {
			return false
		}
		users = append(users, ID)
	}

	conv := mom.findConversationByUsers(users)
	var (
		dm  *slack.Channel
		err error
	)
	if conv == nil {
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
		mom.rtm.SendMessage(mom.rtm.NewOutgoingMessage(mom.getMsg("highVolumeError"), chanID, slack.RTMsgOptionTS(threadID)))
		return false
	}
	return true
}
