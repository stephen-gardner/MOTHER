package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
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

var commands map[string]func(mom *Mother, params cmdParams) bool

func initCommands() {
	commands = make(map[string]func(mom *Mother, params cmdParams) bool)
	commands["active"] = cmdActive
	commands["blacklist"] = cmdBlacklist
	commands["close"] = cmdClose
	commands["contact"] = cmdContact
	commands["help"] = cmdHelp
	commands["invite"] = cmdInvite
	commands["load"] = cmdLoad
	commands["logs"] = cmdLogs
	commands["reload"] = cmdReload
	commands["resume"] = cmdResume
	commands["unload"] = cmdUnload
	commands["uptime"] = cmdUptime
}

func getSlackID(tagged string) string {
	rgx := regexp.MustCompile("<@(.*?)>")
	res := rgx.FindStringSubmatch(tagged)
	if res == nil {
		return ""
	}
	return res[1]
}

func cmdActive(mom *Mother, params cmdParams) bool {
	active := []string{mom.getMsg("listActive")}
	for _, conv := range mom.Conversations {
		if conv.active {
			tagged := make([]string, 0)
			for _, slackID := range strings.Split(conv.SlackIDs, ",") {
				tagged = append(tagged, fmt.Sprintf("<@%s>", slackID))
			}
			timeout := time.Duration(mom.config.SessionTimeout) * time.Second
			timeout -= time.Now().Sub(conv.UpdatedAt)
			line := fmt.Sprintf(
				mom.getMsg("listActiveElement"),
				mom.getMessageLink(conv.ThreadID),
				strings.Join(tagged, ", "),
				timeout.Round(time.Second),
			)
			active = append(active, line)
		}
	}
	if len(active) == 1 {
		active = append(active, mom.getMsg("listNone"))
	}
	out := strings.Join(active, "\n")
	mom.rtm.SendMessage(mom.rtm.NewOutgoingMessage(out, params.chanID, slack.RTMsgOptionTS(params.threadID)))
	return true
}

func cmdBlacklist(mom *Mother, params cmdParams) bool {
	if len(params.args) == 0 {
		tagged := make([]string, 0)
		for _, bu := range mom.BlacklistedUsers {
			tagged = append(tagged, fmt.Sprintf("<@%s>", bu.SlackID))
		}
		msg := mom.getMsg("listBlacklisted") + strings.Join(tagged, ", ")
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
	for _, tagged := range params.args {
		ID := getSlackID(tagged)
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

func cmdHelp(mom *Mother, params cmdParams) bool {
	var out string
	if len(params.args) == 0 {
		help := make([]string, 0)
		for cmd := range commands {
			key := "cmdHelp" + strings.ToUpper(cmd[0:1]) + cmd[1:]
			if lang := mom.getMsg(key); lang != "" {
				help = append(help, lang)
			}
		}
		sort.Strings(help)
		out = mom.getMsg("cmdHelp") + strings.Join(help, "\n")
	} else {
		cmd := params.args[0]
		if _, present := commands[cmd]; !present {
			return false
		}
		key := "cmdHelp" + strings.ToUpper(cmd[0:1]) + cmd[1:]
		out = mom.getMsg(key) + "\n"
	}
	mom.rtm.SendMessage(mom.rtm.NewOutgoingMessage(out, params.chanID, slack.RTMsgOptionTS(params.threadID)))
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

// Loads bot with given name
func cmdLoad(mom *Mother, params cmdParams) bool {
	if len(params.args) == 0 {
		return false
	}
	botName := params.args[0]
	if _, present := mothers.Load(botName); present {
		return false
	}
	path := filepath.Join("bot_config", botName+".json")
	configFile, err := os.Stat(path)
	if err != nil {
		mom.log.Println(err)
		return false
	}
	return loadBot(configFile)
}

// Reloads bot with updated configuration
func cmdReload(mom *Mother, _ cmdParams) bool {
	path := filepath.Join("bot_config", mom.Name) + ".json"
	configFile, err := os.Stat(path)
	if err != nil {
		mom.log.Println(err)
		return false
	}
	if err := mom.rtm.Disconnect(); err != nil {
		mom.log.Println(err)
		return false
	}
	go func(mom *Mother, configFile os.FileInfo) {
		for mom.online {
			time.Sleep(time.Second)
		}
		loadBot(configFile)
	}(mom, configFile)
	return true
}

// Unloads bot with given name
func cmdUnload(mom *Mother, params cmdParams) bool {
	var botName string
	if len(params.args) == 0 {
		botName = mom.Name
	} else {
		botName = params.args[0]
	}
	bot, present := mothers.Load(botName)
	if !present {
		return false
	}
	unload := bot.(*Mother)
	if err := unload.rtm.Disconnect(); err != nil {
		unload.log.Println(err)
	}
	mothers.Delete(botName)
	return true
}

// Display information about how long each bot has been active
func cmdUptime(mom *Mother, params cmdParams) bool {
	uptime := []string{mom.getMsg("listUptime")}
	mothers.Range(func(key, value interface{}) bool {
		name := key.(string)
		bot := value.(*Mother)
		msg := ""
		// Can't tag bots located in different workspaces
		if mom.rtm.GetInfo().Team.ID == bot.rtm.GetInfo().Team.ID {
			msg = mom.getMsg("listUptimeElement")
		} else {
			msg = mom.getMsg("listUptimeForeignElement")
		}
		info := fmt.Sprintf(msg, name, bot.rtm.GetInfo().User.ID, time.Now().Sub(bot.startedAt).Round(time.Second))
		uptime = append(uptime, info)
		return true
	})
	out := strings.Join(uptime, "\n")
	mom.rtm.SendMessage(mom.rtm.NewOutgoingMessage(out, params.chanID, slack.RTMsgOptionTS(params.threadID)))
	return true
}
