package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nlopes/slack"
)

type (
	convInfo struct {
		threadID  string
		userIDs   []string
		timestamp string
	}

	mother struct {
		name       string
		rtm        *slack.RTM
		chanID     string
		convos     map[string]*conversation
		chanInfo   map[string]*slack.Channel
		online     bool
		lastUpdate int64
	}
)

func newMother(token, name, chanID string) *mother {
	api := slack.New(token,
		slack.OptionDebug(true),
		slack.OptionLog(log.New(os.Stdout, name+": ", log.Lshortfile|log.LstdFlags)),
	)
	rtm := api.NewRTM()
	go rtm.ManageConnection()

	mom := &mother{
		name:       name,
		rtm:        rtm,
		chanID:     chanID,
		online:     true,
		lastUpdate: time.Now().Unix(),
	}
	mom.convos = make(map[string]*conversation)
	mom.chanInfo = make(map[string]*slack.Channel)
	return mom
}

func (mom *mother) addConversation(conv *conversation) {
	prev, present := mom.convos[conv.dmID]
	mom.convos[conv.dmID] = conv
	if present {
		var link string

		link = mom.getMessageLink(conv.threadID)
		prev.sendMessageToThread(fmt.Sprintf(sessionContextSwitchedTo, link))
		link = mom.getMessageLink(prev.threadID)
		conv.sendMessageToThread(fmt.Sprintf(sessionContextSwitchedFrom, link))
		if err := prev.save(); err != nil {
			log.Println(err)
			mom.convos[prev.threadID+strconv.FormatInt(prev.lastUpdate, 10)] = prev
		}
	}
}

func (mom *mother) newConversation(dmID, threadID string, userIDs []string) *conversation {
	sort.Strings(userIDs)
	conv := conversation{
		mom:      mom,
		dmID:     dmID,
		threadID: threadID,
		userIDs:  userIDs,
	}
	conv.logs = make(map[string]logEntry)
	conv.convIndex = make(map[string]string)
	conv.directIndex = make(map[string]string)
	conv.editedLogs = make([]logEntry, 0)
	conv.update()
	return &conv
}

func (mom *mother) startConversation(userIDs []string, dmID string, notifyUser bool) (*conversation, error) {
	var sb strings.Builder

	first := true
	for _, ID := range userIDs {
		if ID == mom.rtm.GetInfo().User.ID {
			continue
		}
		if first {
			first = false
		} else {
			sb.WriteString(", ")
		}
		sb.WriteString(fmt.Sprintf("<@%s>", ID))
	}
	notice := fmt.Sprintf(sessionNotice, sb.String())
	timestamp, err := mom.postMessage(mom.chanID, notice, "")
	if err != nil {
		return nil, err
	}

	conv := mom.newConversation(dmID, timestamp, userIDs)
	mom.addConversation(conv)
	if notifyUser {
		conv.sendMessageToDM(sessionStart)
	}
	return conv, nil
}

func (mom *mother) reapConversations(sessionTimeout int64) {
	epoch := time.Now().Unix()
	for key, conv := range mom.convos {
		if epoch-conv.lastUpdate < sessionTimeout {
			continue
		}
		err := conv.save()
		if err != nil {
			log.Println(err)
			continue
		}
		delete(mom.convos, key)
		delete(mom.chanInfo, key)
		conv.sendExpirationNotice()
	}
}

func (mom *mother) findConversation(timestamp string, loadExpired bool) *conversation {
	for _, conv := range mom.convos {
		if conv.hasLog(timestamp) {
			return conv
		}
	}

	if !loadExpired {
		return nil
	}

	conv, err := mom.loadConversation(timestamp)
	if err != nil {
		log.Println(err)
		return nil
	}
	return conv
}

func (mom *mother) findConversationByUsers(userIDs []string) *conversation {
	sort.Strings(userIDs)
	for _, conv := range mom.convos {
		if reflect.DeepEqual(userIDs, conv.userIDs) {
			return conv
		}
	}
	return nil
}

func (mom *mother) loadConversation(threadID string) (*conversation, error) {
	var userIDs string

	query := fmt.Sprintf(findThreadIndex, mom.chanID)
	stmt, err := db.Prepare(query)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	result, err := stmt.Query(threadID)
	if err != nil {
		return nil, err
	}
	defer result.Close()
	if !result.Next() {
		return nil, nil
	}
	err = result.Scan(&userIDs)
	if err != nil {
		return nil, err
	}

	users := strings.Split(userIDs, ",")
	for _, user := range users {
		if mom.hasMember(user) {
			return nil, nil
		}
	}

	params := slack.OpenConversationParameters{
		ChannelID: "",
		ReturnIM:  true,
		Users:     users,
	}
	channel, _, _, err := mom.rtm.OpenConversation(&params)
	if err != nil {
		return nil, err
	}

	conv := mom.newConversation(channel.ID, threadID, users)
	conv.resume()
	return conv, nil
}

func (mom *mother) getChannelInfo(chanID string) *slack.Channel {
	chanInfo, present := mom.chanInfo[chanID]
	if present {
		return chanInfo
	}
	chanInfo, err := mom.rtm.GetConversationInfo(chanID, false)
	if err != nil {
		log.Println(err)
		if !present {
			return nil
		}
	}

	params := slack.GetUsersInConversationParameters{
		ChannelID: chanID,
		Cursor:    "",
		Limit:     0,
	}
	members, _, err := mom.rtm.GetUsersInConversation(&params)
	if err != nil {
		log.Println(err)
		if len(chanInfo.Members) == 0 {
			return nil
		}
	} else {
		chanInfo.Members = members
	}

	mom.chanInfo[chanID] = chanInfo
	return chanInfo
}

func (mom *mother) getMessageLink(timestamp string) string {
	params := slack.PermalinkParameters{
		Channel: mom.chanID,
		Ts:      timestamp,
	}
	link, err := mom.rtm.GetPermalink(&params)
	if err != nil {
		return timestamp
	}
	return link
}

func (mom *mother) hasMember(userID string) bool {
	chanInfo := mom.getChannelInfo(mom.chanID)
	if chanInfo != nil {
		for _, member := range chanInfo.Members {
			if userID == member {
				return true
			}
		}
	}
	return false
}

func (mom *mother) lookupLogs(id string, isUser bool) ([]logEntry, error) {
	var query string

	if isUser {
		query = fmt.Sprintf(lookupLogsUser, mom.chanID, mom.chanID)
		id = "%" + id + "%"
	} else {
		query = fmt.Sprintf(lookupLogsThread, mom.chanID)
	}

	stmt, err := db.Prepare(query)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	result, err := stmt.Query(id)
	if err != nil {
		return nil, err
	}
	defer result.Close()

	logs := make([]logEntry, 0)
	for result.Next() {
		var entry logEntry

		err = result.Scan(&entry.userID, &entry.msg, &entry.timestamp, &entry.original)
		if err != nil {
			return nil, err
		}
		logs = append(logs, entry)
	}
	return logs, nil
}

func (mom *mother) lookupThreads(userID string, page int) ([]convInfo, error) {
	var (
		query  string
		stmt   *sql.Stmt
		result *sql.Rows
		err    error
	)

	if userID != "" {
		query = fmt.Sprintf(lookupThreadsUser, mom.chanID)
		stmt, err = db.Prepare(query)
		if err != nil {
			return nil, err
		}
		defer stmt.Close()
		result, err = stmt.Query("%"+userID+"%", 10, 10*(page-1))
	} else {
		query = fmt.Sprintf(lookupThreads, mom.chanID)
		stmt, err = db.Prepare(query)
		if err != nil {
			return nil, err
		}
		defer stmt.Close()
		result, err = stmt.Query(10, 10*(page-1))
	}
	if err != nil {
		return nil, err
	}
	defer result.Close()

	threads := make([]convInfo, 0)
	for result.Next() {
		var (
			info    convInfo
			userIDs string
		)

		err = result.Scan(&info.threadID, &userIDs, &info.timestamp)
		if err != nil {
			return nil, err
		}
		info.userIDs = strings.Split(userIDs, ",")
		threads = append(threads, info)
	}
	return threads, nil
}

// If we get errors too often, we may have to develop more robust message flow
func (mom *mother) postMessage(chanID, msg, threadID string) (string, error) {
	var err error
	for x := 0; x < 5; x++ {
		_, timestamp, err := mom.rtm.PostMessage(
			chanID,
			slack.MsgOptionText(msg, false),
			slack.MsgOptionTS(threadID),
		)
		if err == nil {
			return timestamp, nil
		} else if strings.HasPrefix(err.Error(), "slack rate limit exceeded") {
			time.Sleep(2 * time.Second)
		} else {
			break
		}
	}
	log.Println(err)
	out := mom.rtm.NewOutgoingMessage(
		highVolumeError,
		mom.chanID,
		slack.RTMsgOptionTS(threadID),
	)
	mom.rtm.SendMessage(out)
	return "", err
}

func (mom *mother) shutdown() {
	mom.online = false
	mom.rtm.Disconnect()
	log.Println(mom.name + " disconnected")
}
