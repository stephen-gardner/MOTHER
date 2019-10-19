package main

import (
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jinzhu/gorm"
	"github.com/nlopes/slack"
)

type (
	Mother struct {
		gorm.Model
		Name             string
		Conversations    []Conversation
		BlacklistedUsers []BlacklistedUser
		config           botConfig                 `gorm:"-"`
		log              *log.Logger               `gorm:"-"`
		rtm              *slack.RTM                `gorm:"-"`
		chanInfo         map[string]*slack.Channel `gorm:"-"`
		usersInfo        map[string]*slack.User    `gorm:"-"`
		online           bool                      `gorm:"-"`
	}

	BlacklistedUser struct {
		gorm.Model
		MotherID uint
		SlackID  string
	}
)

func getMother(config botConfig) *Mother {
	logger := log.New(os.Stdout, config.Name+": ", log.Lshortfile|log.LstdFlags)
	rtm := slack.New(config.Token, slack.OptionDebug(true), slack.OptionLog(logger)).NewRTM()
	go rtm.ManageConnection()

	mom := &Mother{
		config:    config,
		log:       logger,
		rtm:       rtm,
		chanInfo:  make(map[string]*slack.Channel),
		usersInfo: make(map[string]*slack.User),
		online:    true,
	}

	// Load conversations that should still be active
	threshold := time.Now().Add(-(time.Duration(mom.config.SessionTimeout) * time.Second))
	q := db.Where("name = ?", config.Name)
	q = q.Preload("BlacklistedUsers")
	q = q.Preload("Conversations", "last_updated > ?", threshold)
	q = q.Preload("Conversations.MessageLogs")
	if err := q.FirstOrCreate(mom).Error; err != nil {
		mom.log.Fatal(err)
	}

	return mom
}

func (mom *Mother) isBlacklisted(slackID string) bool {
	for _, bu := range mom.BlacklistedUsers {
		if bu.SlackID == slackID {
			return true
		}
	}
	return false
}

func (mom *Mother) blacklistUser(slackID string) bool {
	if mom.isBlacklisted(slackID) {
		return false
	}
	bu := BlacklistedUser{
		MotherID: mom.ID,
		SlackID:  slackID,
	}
	if err := db.Model(mom).Association("BlacklistedUsers").Append(bu).Error; err != nil {
		mom.log.Println(err)
		return false
	}
	return true
}

func (mom *Mother) removeBlacklistedUser(slackID string) bool {
	for _, bu := range mom.BlacklistedUsers {
		if bu.SlackID == slackID {
			if err := db.Model(mom).Association("BlacklistedUsers").Delete(bu).Error; err == nil {
				mom.log.Println(err)
				return false
			}
			return true
		}
	}
	return false
}

func (mom *Mother) beginConversation(directID string, slackIDs []string, notifyUsers bool) (*Conversation, error) {
	var sb strings.Builder
	first := true
	for _, ID := range slackIDs {
		if first {
			first = false
		} else {
			sb.WriteString(", ")
		}
		sb.WriteString(fmt.Sprintf("<@%s>", ID))
	}
	notice := fmt.Sprintf(mom.getMsg("sessionNotice"), sb.String())
	threadID, err := mom.postMessage(mom.config.ChanID, "", notice)
	if err != nil {
		return nil, err
	}
	conv := &Conversation{
		MotherID:    mom.ID,
		SlackIDs:    strings.Join(slackIDs, ","),
		DirectID:    directID,
		ThreadID:    threadID,
		mom:         mom,
		convIndex:   make(map[string]string),
		directIndex: make(map[string]string),
	}
	if err := mom.trackConversation(conv); err != nil {
		if _, _, err := mom.rtm.DeleteMessage(mom.config.ChanID, threadID); err != nil {
			// In the worst case, this could result in an ugly situation where channel members are unknowingly sending
			// messages to an inactive thread, but the chances of this many things suddenly going wrong is extremely
			// unlikely
			mom.log.Println(err)
		}
		return nil, err
	}
	if notifyUsers {
		conv.sendMessageToDM(mom.getMsg("sessionStart"))
	}
	return conv, nil
}

func (mom *Mother) trackConversation(conv *Conversation) error {
	var prev *Conversation
	for _, p := range mom.Conversations {
		if p.active && p.DirectID == conv.DirectID {
			prev = &p
			break
		}
	}
	if err := db.Model(mom).Association("Conversations").Append(conv).Error; err != nil {
		return err
	}
	conv.active = true
	if prev == nil {
		return nil
	}

	prev.active = false
	if err := db.Model(mom).Association("Conversations").Delete(prev).Error; err != nil {
		// We can let the conversation reaper clean this up if it fails
		mom.log.Println(err)
	}

	link := mom.getMessageLink(conv.ThreadID)
	prev.sendMessageToThread(fmt.Sprintf(mom.getMsg("sessionContextSwitchedTo"), link))
	link = mom.getMessageLink(prev.ThreadID)
	conv.sendMessageToThread(fmt.Sprintf(mom.getMsg("sessionContextSwitchedFrom"), link))
	return nil
}

func (mom *Mother) findConversation(timestamp string, loadExpired bool) *Conversation {
	for _, conv := range mom.Conversations {
		if conv.hasLog(timestamp) {
			return &conv
		}
	}
	if !loadExpired {
		return nil
	}
	conv, err := mom.loadConversation(timestamp)
	if err != nil {
		mom.log.Println(err)
		return nil
	}
	return conv
}

func (mom *Mother) findConversationByUsers(slackIDs []string) *Conversation {
	seeking := strings.Join(slackIDs, ",")
	for _, conv := range mom.Conversations {
		if seeking == conv.SlackIDs {
			return &conv
		}
	}
	return nil
}

func (mom *Mother) loadConversation(threadID string) (*Conversation, error) {
	conv := &Conversation{}
	if err := db.Where("thread_id = ?", threadID).Preload("MessageLogs").First(conv).Error; err != nil {
		if gorm.IsRecordNotFoundError(err) {
			return nil, nil
		}
		return nil, err
	}
	for _, slackID := range strings.Split(conv.SlackIDs, ",") {
		// Prevent reactivating conversations with channel members or blacklisted users
		if mom.hasMember(slackID) || mom.isBlacklisted(slackID) {
			return nil, nil
		}
	}
	if _, _, _, err := mom.rtm.OpenConversation(
		&slack.OpenConversationParameters{
			ChannelID: conv.DirectID,
			ReturnIM:  true,
			Users:     nil,
		},
	); err != nil {
		return nil, err
	}
	if err := conv.mom.trackConversation(conv); err != nil {
		return nil, err
	}
	conv.sendMessageToThread(conv.mom.getMsg("sessionResumeConv"))
	conv.sendMessageToDM(conv.mom.getMsg("sessionResumeDirect"))
	return conv, nil
}

func (mom *Mother) reapConversations() {
	epoch := time.Now()
	for _, conv := range mom.Conversations {
		if conv.active && epoch.Sub(conv.UpdatedAt) < time.Duration(mom.config.SessionTimeout) {
			continue
		}
		conv.active = false
		if err := db.Model(mom).Association("Conversations").Delete(&conv).Error; err != nil {
			// Try to remove it again next reaping
			mom.log.Println(err)
			continue
		}
		delete(mom.chanInfo, conv.DirectID)
		conv.sendMessageToDM(conv.mom.getMsg("sessionExpiredDirect"))
		conv.sendMessageToThread(fmt.Sprintf(conv.mom.getMsg("sessionExpiredConv"), conv.ThreadID))
	}
}

func (mom *Mother) getChannelInfo(chanID string) *slack.Channel {
	if chanInfo, present := mom.chanInfo[chanID]; present {
		return chanInfo
	}
	chanInfo, err := mom.rtm.GetConversationInfo(chanID, false)
	if err != nil {
		mom.log.Println(err)
		return nil
	}
	members, _, err := mom.rtm.GetUsersInConversation(
		&slack.GetUsersInConversationParameters{
			ChannelID: chanID,
			Cursor:    "",
			Limit:     0,
		},
	)
	if err != nil {
		mom.log.Println(err)
		return nil
	}
	// Filter out the bot's slack ID from the list
	for i, slackID := range members {
		if slackID == mom.rtm.GetInfo().User.ID {
			members = append(members[:i], members[i+1:]...)
			break
		}
	}
	sort.Strings(members)
	chanInfo.Members = members
	mom.chanInfo[chanID] = chanInfo
	return chanInfo
}

func (mom *Mother) getUserInfo(slackID string) *slack.User {
	if userInfo, present := mom.usersInfo[slackID]; present {
		return userInfo
	}
	userInfo, err := mom.rtm.GetUserInfo(slackID)
	if err != nil {
		mom.log.Println(err)
		return nil
	}
	mom.usersInfo[slackID] = userInfo
	return userInfo
}

func (mom *Mother) hasMember(slackID string) bool {
	chanInfo := mom.getChannelInfo(mom.config.ChanID)
	if chanInfo != nil {
		for _, member := range chanInfo.Members {
			if member == slackID {
				return true
			}
		}
	}
	return false
}

func (mom *Mother) getMessageLink(timestamp string) string {
	link, err := mom.rtm.GetPermalink(
		&slack.PermalinkParameters{
			Channel: mom.config.ChanID,
			Ts:      timestamp,
		},
	)
	if err != nil {
		return timestamp
	}
	return fmt.Sprintf("<%s|%s>", link, timestamp)
}

func (mom *Mother) postMessage(chanID, threadID, msg string) (string, error) {
	var timestamp string
	var err error
	for x := 0; timestamp == "" && x < 5; x++ {
		_, timestamp, err = mom.rtm.PostMessage(
			chanID,
			slack.MsgOptionText(msg, false),
			slack.MsgOptionTS(threadID),
		)
		if err != nil && strings.HasPrefix(err.Error(), "slack rate limit exceeded") {
			// Should be plenty enough time to recover from a rate limit on this thread, but
			// may have to switch to some sort of message queue if it doesn't work out.
			// Could be not good to freeze the entire thread if there's heavy traffic...
			time.Sleep(2 * time.Second)
		}
	}
	return timestamp, err
}

func (mom *Mother) getMsg(key string) string {
	return mom.config.Lang[key]
}
