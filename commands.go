package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/nlopes/slack"
)

type cmdParams struct {
	chanID   string
	threadID string
	userID   string
	args     []string
}

var commands = map[string]func(mom *Mother, params cmdParams) bool{
	"blacklist": cmdBlacklist,
	"invite":    cmdInvite,
}

func getSlackID(tagged string) string {
	rgx := regexp.MustCompile("<@(.*?)>")
	res := rgx.FindStringSubmatch(tagged)
	if res == nil {
		return ""
	}
	return res[1]
}

func cmdBlacklist(mom *Mother, params cmdParams) bool {
	if len(params.args) == 0 {
		tagged := make([]string, 0)
		for _, bu := range mom.BlacklistedUsers {
			tagged = append(tagged, fmt.Sprintf("<@%s>", bu.SlackID))
		}
		msg := fmt.Sprintf(mom.getMsg("listBlacklisted"), strings.Join(tagged, ", "))
		mom.rtm.SendMessage(mom.rtm.NewOutgoingMessage(msg, params.chanID, slack.RTMsgOptionTS(params.threadID)))
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
	slackIDs := make([]string, 0)
	for _, tagged := range params.args {
		ID := getSlackID(tagged)
		listed := mom.isBlacklisted(ID)
		if ID == "" || (rm && !listed) || (!rm && listed) {
			return false
		}
		slackIDs = append(slackIDs, ID)
	}
	res := true
	for _, ID := range slackIDs {
		if rm {
			if !mom.removeBlacklistedUser(ID) {
				res = false
			}
		} else {
			if !mom.blacklistUser(ID) {
				res = false
			}
		}
	}
	return res
}

func cmdInvite(mom *Mother, params cmdParams) bool {
	if len(params.args) == 0 {
		return false
	}
	slackIDs := make([]string, 0)
	for _, tagged := range params.args {
		ID := getSlackID(tagged)
		if ID == "" || mom.hasMember(ID) {
			return false
		}
		slackIDs = append(slackIDs, ID)
	}
	mom.invited = append(mom.invited, slackIDs...)
	_, err := mom.rtm.InviteUsersToConversation(mom.config.ChanID, slackIDs...)
	if err != nil {
		mom.log.Println(err)
	}
	return true
}
