package main

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jinzhu/gorm"
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
	"close":     cmdClose,
	"contact":   cmdContact,
	"invite":    cmdInvite,
	"logs":      cmdLogs,
	"resume":    cmdResume,
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

func cmdClose(mom *Mother, params cmdParams) bool {
	if len(params.args) == 0 {
		return false
	}
	var conv *Conversation
	ID := getSlackID(params.args[0])
	if len(params.args) == 1 && ID == "" {
		conv = mom.findConversationByTimestamp(params.args[0], false)
	} else if ID != "" {
		slackIDs := make([]string, 0)
		for _, tagged := range params.args {
			ID = getSlackID(tagged)
			if ID == "" {
				return false
			}
			slackIDs = append(slackIDs, ID)
		}
		conv = mom.findConversationByUsers(slackIDs)
	}
	if conv == nil {
		return false
	}
	conv.expire()
	mom.reapConversations()
	return true
}

func cmdContact(mom *Mother, params cmdParams) bool {
	if len(params.args) == 0 {
		return false
	}
	slackIDs := make([]string, 0)
	for _, tag := range params.args {
		ID := getSlackID(tag)
		if ID == "" || mom.hasMember(ID) || mom.isBlacklisted(ID) {
			return false
		}
		slackIDs = append(slackIDs, ID)
	}
	if conv := mom.findConversationByUsers(slackIDs); conv == nil {
		dm, _, _, err := mom.rtm.OpenConversation(
			&slack.OpenConversationParameters{
				ChannelID: "",
				ReturnIM:  true,
				Users:     slackIDs,
			},
		)
		if err != nil {
			mom.log.Println(err)
			return false
		}
		if _, err := mom.createConversation(dm.ID, slackIDs, true); err != nil {
			mom.log.Println(err)
			return false
		}
	} else {
		// If an active conversation already exists, !contact simply spawns a fresher one
		if _, err := mom.createConversation(conv.DirectID, slackIDs, false); err != nil {
			mom.log.Println(err)
			return false
		}
	}
	return true
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

func cmdLogs(mom *Mother, params cmdParams) bool {
	if len(params.args) == 0 {
		return false
	}
	var convos []Conversation
	ID := getSlackID(params.args[0])
	if len(params.args) == 1 && ID == "" {
		q := db.Where("thread_id = ?", params.args[0])
		q = q.Preload("MessageLogs")
		if err := q.Find(&convos).Error; err != nil {
			if err != gorm.ErrRecordNotFound {
				mom.log.Println(err)
			}
			return false
		}
	} else if ID != "" {
		slackIDs := make([]string, 0)
		for _, tagged := range params.args {
			ID = getSlackID(tagged)
			if ID == "" {
				return false
			}
			slackIDs = append(slackIDs, ID)
		}
		sort.Strings(slackIDs)
		q := db.Where("slack_ids LIKE ?", strings.Join(slackIDs, ","))
		q = q.Preload("MessageLogs")
		if err := q.Find(&convos).Error; err != nil {
			if err != gorm.ErrRecordNotFound {
				mom.log.Println(err)
			}
			return false
		}
	}
	buff := &bytes.Buffer{}
	first := true
	for _, conv := range convos {
		if !first {
			buff.WriteRune('\n')
		}
		buff.WriteString(fmt.Sprintf(mom.getMsg("logThread"), conv.ThreadID))
		for _, msg := range conv.MessageLogs {
			if msg.Msg != "" {
				userInfo, err := mom.getUserInfo(msg.SlackID)
				if err != nil {
					mom.log.Println(err)
					return false
				}
				epoch, _ := strconv.ParseInt(strings.Split(msg.ConvTimestamp, ".")[0], 10, 64)
				timestamp := time.Unix(epoch, 0).String()
				format := ""
				if msg.Original {
					format = mom.getMsg("logMsg")
				} else {
					format = mom.getMsg("logMsgEdited")
				}
				buff.WriteString(fmt.Sprintf(format, timestamp, userInfo.Profile.DisplayName, msg.Msg))
			}
		}
		first = false
	}
	_, err := mom.rtm.UploadFile(
		slack.FileUploadParameters{
			Reader:          buff,
			Filename:        "Logs.txt",
			Channels:        []string{params.chanID},
			ThreadTimestamp: params.threadID,
		},
	)
	if err != nil {
		mom.log.Println(err)
		return false
	}
	return true
}

func cmdResume(mom *Mother, params cmdParams) bool {
	if len(params.args) == 0 {
		return false
	}
	var conv *Conversation
	ID := getSlackID(params.args[0])
	if len(params.args) == 1 && ID == "" {
		if conv = mom.findConversationByTimestamp(params.args[0], true); conv == nil {
			return false
		}
	} else if ID != "" {
		slackIDs := make([]string, 0)
		for _, tagged := range params.args {
			ID = getSlackID(tagged)
			if ID == "" || mom.hasMember(ID) || mom.isBlacklisted(ID) {
				return false
			}
			slackIDs = append(slackIDs, ID)
		}
		if conv = mom.findConversationByUsers(slackIDs); conv == nil {
			var err error
			conv = &Conversation{}
			sort.Strings(slackIDs)
			q := db.Where("slack_ids = ?", strings.Join(slackIDs, ","))
			q = q.Order("updated_at desc").First(conv)
			if err = q.Error; err != nil {
				if err != gorm.ErrRecordNotFound {
					mom.log.Println(err)
				}
				return false
			}
			if conv, err = mom.loadConversation(conv.ThreadID); err != nil {
				mom.log.Println(err)
				return false
			}
		}
	} else {
		return false
	}
	_, err := mom.createConversation(conv.DirectID, strings.Split(conv.SlackIDs, ","), false)
	if err != nil {
		mom.log.Println(err)
		return false
	}
	return true
}
