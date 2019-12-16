package main

import (
	"bytes"
	"fmt"
	"math"
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
	commands = map[string]func(mom *Mother, params cmdParams) bool{
		"active":    cmdActive,
		"blacklist": cmdBlacklist,
		"close":     cmdClose,
		"contact":   cmdContact,
		"help":      cmdHelp,
		"history":   cmdHistory,
		"invite":    cmdInvite,
		"load":      cmdLoad,
		"logs":      cmdLogs,
		"reload":    cmdReload,
		"resume":    cmdResume,
		"unload":    cmdUnload,
		"uptime":    cmdUptime,
	}
}

func getSlackID(tagged string) string {
	rgx := regexp.MustCompile("<@(.*?)>")
	res := rgx.FindStringSubmatch(tagged)
	if res == nil {
		return ""
	}
	return res[1]
}

// Lists currently active conversations
func cmdActive(mom *Mother, params cmdParams) bool {
	active := make([]string, len(mom.Conversations)+1)
	active[0] = mom.getMsg("cmdActive", nil)
	i := 1
	for _, conv := range mom.Conversations {
		if !conv.Active {
			continue
		}
		tagged := strings.Split(conv.SlackIDs, ",")
		for i, ID := range tagged {
			tagged[i] = fmt.Sprintf("<@%s>", ID)
		}
		// Get how much time is left before conversation expires
		timeout := time.Duration(mom.config.SessionTimeout) * time.Second
		timeout -= time.Now().Sub(conv.UpdatedAt)
		active[i] = mom.getMsg("cmdActiveElement", []langVar{
			{"THREAD_LINK", mom.getMessageLink(conv.ThreadID)},
			{"USER_LIST", strings.Join(tagged, ", ")},
			{"TIME_UNTIL_EXPIRED", timeout.Round(time.Second).String()},
		})
		i++
	}
	active = active[:i]
	if len(active) == 1 {
		active = append(active, mom.getMsg("listNone", nil))
	}
	msg := strings.Join(active, "\n")
	mom.rtm.SendMessage(mom.rtm.NewOutgoingMessage(msg, params.chanID, slack.RTMsgOptionTS(params.threadID)))
	return true
}

func cmdBlacklist(mom *Mother, params cmdParams) bool {
	// Print list of blacklisted users without parameters
	if len(params.args) == 0 {
		tagged := make([]string, len(mom.BlacklistedUsers))
		for i, bu := range mom.BlacklistedUsers {
			tagged[i] = fmt.Sprintf("<@%s>", bu.SlackID)
		}
		// It won't be alphabetical, but at least keeps the list order consistent
		sort.Strings(tagged)
		msg := mom.getMsg("cmdBlacklist", nil) + strings.Join(tagged, ", ")
		mom.rtm.SendMessage(mom.rtm.NewOutgoingMessage(msg, params.chanID, slack.RTMsgOptionTS(params.threadID)))
		return true
	}
	// Flag that the operation is a removal
	rm := false
	if params.args[0] == "rm" {
		if len(params.args) < 2 {
			return false
		}
		rm = true
		params.args = params.args[1:]
	}
	// Build list of slack IDs to add/remove
	// Can not add/remove other bots, the sender, or redundant IDs
	slackIDs := make([]string, 0)
	for _, tagged := range params.args {
		ID := getSlackID(tagged)
		listed := mom.isBlacklisted(ID)
		isBot := ID == "USLACKBOT"
		if !isBot {
			mothers.Range(func(_, value interface{}) bool {
				other := value.(*Mother)
				if other.rtm.GetInfo().Team.ID == mom.rtm.GetInfo().Team.ID {
					if other.rtm.GetInfo().User.ID == ID {
						isBot = true
						return false
					}
				}
				return true
			})
		}
		if ID == "" || ID == params.userID || (!rm && listed) || (rm && (!listed || isBot)) {
			return false
		}
		slackIDs = append(slackIDs, ID)
	}
	var res bool
	for _, ID := range slackIDs {
		if rm {
			res = mom.removeBlacklistedUser(ID)
		} else {
			res = mom.blacklistUser(ID)
		}
	}
	return res
}

// Deactivates conversation specified by threadID/users
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

// Starts conversation with specified users
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
	// If an active conversation already exists, !contact simply spawns a new thread
	if conv := mom.findConversationByUsers(slackIDs); conv != nil {
		_, err := mom.
			newConversation().
			fromCommand(params.userID).
			postNewThread(conv.DirectID, slackIDs).
			create()
		if err != nil {
			mom.log.Println(err)
			return false
		}
		return true
	}
	dm, _, _, err := mom.rtm.OpenConversation(
		&slack.OpenConversationParameters{Users: slackIDs},
	)
	if err != nil {
		mom.log.Println(err)
		return false
	}
	_, err = mom.
		newConversation().
		fromCommand(params.userID).
		postNewThread(dm.ID, slackIDs).
		create()
	if err != nil {
		mom.log.Println(err)
	}
	return err == nil
}

func cmdHelp(mom *Mother, params cmdParams) bool {
	var msg string
	if len(params.args) == 0 {
		help := make([]string, 0)
		for cmd := range commands {
			key := "cmdHelp" + strings.ToUpper(cmd[0:1]) + cmd[1:]
			if lang := mom.getMsg(key, nil); lang != "" {
				help = append(help, lang)
			}
		}
		sort.Strings(help)
		msg = mom.getMsg("cmdHelp", nil) + strings.Join(help, "\n")
	} else {
		cmd := params.args[0]
		if _, present := commands[cmd]; !present {
			return false
		}
		key := "cmdHelp" + strings.ToUpper(cmd[0:1]) + cmd[1:]
		msg = mom.getMsg(key, nil) + "\n"
	}
	mom.rtm.SendMessage(mom.rtm.NewOutgoingMessage(msg, params.chanID, slack.RTMsgOptionTS(params.threadID)))
	return true
}

// Display paginated log of recent threads
func cmdHistory(mom *Mother, params cmdParams) bool {
	var slackIDs []string
	page := 1
	if len(params.args) > 0 {
		for _, tagged := range params.args {
			ID := getSlackID(tagged)
			if ID == "" {
				break
			}
			slackIDs = append(slackIDs, ID)
		}
		sort.Strings(slackIDs)
		params.args = params.args[len(slackIDs):]
	}
	if len(params.args) > 0 {
		var err error
		if page, err = strconv.Atoi(params.args[0]); err != nil || page < 1 {
			return false
		}
	}
	var convos []Conversation
	var err error
	var totalRecords uint
	if len(slackIDs) > 0 {
		err = db.
			Model(&Conversation{}).
			Where("mother_id = ? AND slack_ids = ?", mom.ID, strings.Join(slackIDs, ",")).
			Order("updated_at desc, id desc").
			Count(&totalRecords).
			Limit(mom.config.ThreadsPerPage).
			Offset(mom.config.ThreadsPerPage * (page - 1)).
			Find(&convos).Error
	} else {
		err = db.
			Model(&Conversation{}).
			Where("mother_id = ?", mom.ID, ).
			Order("updated_at desc, id desc").
			Count(&totalRecords).
			Limit(mom.config.ThreadsPerPage).
			Offset(mom.config.ThreadsPerPage * (page - 1)).
			Find(&convos).Error
	}
	if err != nil && err != gorm.ErrRecordNotFound {
		mom.log.Println(err)
		return false
	}
	threads := make([]string, len(convos)+1)
	totalPages := math.Ceil(float64(totalRecords) / float64(mom.config.ThreadsPerPage))
	threads[0] = mom.getMsg("cmdHistory", []langVar{
		{"CURRENT_PAGE", strconv.Itoa(page)},
		{"TOTAL_PAGES", strconv.Itoa(int(totalPages))},
	})
	i := 1
	for _, conv := range convos {
		tagged := strings.Split(conv.SlackIDs, ",")
		for i, ID := range tagged {
			tagged[i] = fmt.Sprintf("<@%s>", ID)
		}
		threads[i] = mom.getMsg("cmdHistoryElement", []langVar{
			{"THREAD_LINK", mom.getMessageLink(conv.ThreadID)},
			{"USER_LIST", strings.Join(tagged, ", ")},
			{"LAST_UPDATED", conv.UpdatedAt.String()},
		})
		i++
	}
	if len(convos) == 0 {
		if page > 1 {
			return false
		}
		threads = append(threads, mom.getMsg("listNone", nil))
	}
	msg := strings.Join(threads, "\n")
	mom.rtm.SendMessage(mom.rtm.NewOutgoingMessage(msg, params.chanID, slack.RTMsgOptionTS(params.threadID)))
	return true
}

// Invites users to member channel
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
	return err == nil
}

// Writes MessageLog slice to buffer
func writeLogs(mom *Mother, buff *bytes.Buffer, logs []MessageLog) error {
	for _, msg := range logs {
		if msg.Msg != "" {
			userInfo, err := mom.getUserInfo(msg.SlackID)
			if err != nil {
				return err
			}
			var format string
			if msg.Original {
				format = "cmdLogsMsg"
			} else {
				format = "cmdLogsMsgEdited"
			}
			epoch, _ := strconv.ParseInt(strings.Split(msg.ConvTimestamp, ".")[0], 10, 64)
			displayName := userInfo.Profile.DisplayName
			if displayName == "" {
				displayName = userInfo.Name
			}
			buff.WriteString(mom.getMsg(format, []langVar{
				{"TIMESTAMP", time.Unix(epoch, 0).String()},
				{"DISPLAY_NAME", displayName},
				{"MESSAGE", msg.Msg},
			}))
		}
	}
	return nil
}

// Builds default !logs output, with logs sorted chronologically into blocks by session
func buildLogsOutput(mom *Mother, buff *bytes.Buffer, convos []Conversation) error {
	first := true
	for _, conv := range convos {
		if !first {
			buff.WriteRune('\n')
		} else {
			first = false
		}
		buff.WriteString(mom.getMsg("cmdLogsThread", []langVar{
			{"THREAD_ID", conv.ThreadID},
		}))
		if err := writeLogs(mom, buff, conv.MessageLogs); err != nil {
			return err
		}
	}
	return nil
}

// Builds !logs output with logs sorted chronologically
func buildMergedLogsOutput(mom *Mother, buff *bytes.Buffer, convos []Conversation) error {
	var logs []MessageLog
	for _, conv := range convos {
		logs = append(logs, conv.MessageLogs...)
	}
	sort.Slice(logs, func(i, j int) bool {
		return logs[i].CreatedAt.Sub(logs[j].CreatedAt) < 0
	})
	return writeLogs(mom, buff, logs)
}

// Upload conversation logs for specified threadID/users
func cmdLogs(mom *Mother, params cmdParams) bool {
	if len(params.args) == 0 {
		return false
	}
	// Flag whether or not log output is merged
	merged := false
	if params.args[0] == "-m" {
		if len(params.args) < 2 {
			return false
		}
		merged = true
		params.args = params.args[1:]
	}
	var convos []Conversation
	var err error
	ID := getSlackID(params.args[0])
	if len(params.args) == 1 && ID == "" {
		err = db.
			Where("mother_id = ? AND thread_id = ?", mom.ID, params.args[0]).
			Preload("MessageLogs").
			Find(&convos).Error
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
		err = db.
			Where("mother_id = ? AND slack_ids LIKE ?", mom.ID, strings.Join(slackIDs, ",")).
			Preload("MessageLogs").
			Find(&convos).Error
	}
	if err != nil {
		if err != gorm.ErrRecordNotFound {
			mom.log.Println(err)
		}
		return false
	}
	buff := &bytes.Buffer{}
	if merged {
		err = buildMergedLogsOutput(mom, buff, convos)
	} else {
		err = buildLogsOutput(mom, buff, convos)
	}
	if err != nil {
		mom.log.Println(err)
		return false
	}
	if buff.Len() == 0 {
		msg := mom.getMsg("cmdLogsNoRecords", nil)
		mom.rtm.SendMessage(mom.rtm.NewOutgoingMessage(msg, params.chanID, slack.RTMsgOptionTS(params.threadID)))
		return true
	}
	_, err = mom.rtm.UploadFile(
		slack.FileUploadParameters{
			Reader:          buff,
			Filename:        "Logs.txt",
			Channels:        []string{params.chanID},
			ThreadTimestamp: params.threadID,
		},
	)
	if err != nil {
		mom.log.Println(err)
	}
	return err == nil
}

// Resumes conversation session specified by threadID/users
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
	} else {
		if ID == "" {
			return false
		}
		slackIDs := make([]string, 0)
		for _, tagged := range params.args {
			ID = getSlackID(tagged)
			if ID == "" || mom.hasMember(ID) || mom.isBlacklisted(ID) {
				return false
			}
			slackIDs = append(slackIDs, ID)
		}
		conv = &Conversation{}
		sort.Strings(slackIDs)
		err := db.
			Where("mother_id = ? AND slack_ids = ?", mom.ID, strings.Join(slackIDs, ",")).
			Order("updated_at desc, id desc").
			First(conv).Error
		if err != nil {
			if err != gorm.ErrRecordNotFound {
				mom.log.Println(err)
			}
			return false
		}
	}
	_, err := mom.
		newConversation().
		loadConversation(conv.ThreadID).
		postNewThread("", nil).
		create()
	if err != nil {
		if err != gorm.ErrRecordNotFound && err != ErrUserNotAllowed {
			mom.log.Println(err)
		}
	}
	return err == nil
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
	res := loadBot(configFile)
	if res {
		go mothers.Range(blacklistBots)
	}
	return res
}

// Reloads bot with updated configuration
func cmdReload(mom *Mother, _ cmdParams) bool {
	path := filepath.Join("bot_config", mom.Name) + ".json"
	configFile, err := os.Stat(path)
	if err != nil {
		mom.log.Println(err)
		return false
	}
	go func(mom *Mother, configFile os.FileInfo) {
		// Give a second for emoji response to send
		time.Sleep(time.Second)
		mom.reload = true
		mom.rtm.Disconnect()
		// Wait for bot to fully disconnect
		<-mom.shutdown
		// Need a little time to prevent new instance from picking up duplicate events
		time.Sleep(time.Second * 5)
		if loadBot(configFile) {
			go mothers.Range(blacklistBots)
		} else {
			// Clean up disabled bot in the event of an error
			mothers.Delete(mom.Name)
		}
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
	toUnload := bot.(*Mother)
	go toUnload.rtm.Disconnect()
	return true
}

// Display information about how long each bot has been active
func cmdUptime(mom *Mother, params cmdParams) bool {
	uptime := []string{mom.getMsg("cmdUptime", nil)}
	mothers.Range(func(key, value interface{}) bool {
		name := key.(string)
		bot := value.(*Mother)
		// Can't tag bots located in different workspaces
		var format string
		if mom.rtm.GetInfo().Team.ID == bot.rtm.GetInfo().Team.ID {
			format = "cmdUptimeElement"
		} else {
			format = "cmdUptimeForeignElement"
		}
		var duration string
		if bot.isOnline() {
			duration = time.Now().Sub(bot.connectedAt).Round(time.Second).String()
		} else {
			duration = mom.getMsg("cmdUptimeOffline", nil)
		}
		uptime = append(uptime, mom.getMsg(format, []langVar{
			{"BOT_NAME", name},
			{"BOT_SLACK_ID", bot.rtm.GetInfo().User.ID},
			{"UPTIME", duration},
		}))
		return true
	})
	msg := strings.Join(uptime, "\n")
	mom.rtm.SendMessage(mom.rtm.NewOutgoingMessage(msg, params.chanID, slack.RTMsgOptionTS(params.threadID)))
	return true
}
