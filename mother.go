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
		events           chan slack.RTMEvent       `gorm:"-"`
		chanInfo         map[string]*slack.Channel `gorm:"-"`
		usersInfo        map[string]*slack.User    `gorm:"-"`
		invited          []string                  `gorm:"-"`
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
	rtm := slack.New(config.Token, slack.OptionDebug(false), slack.OptionLog(logger)).NewRTM()
	go rtm.ManageConnection()
	mom := &Mother{
		Name:      config.Name,
		config:    config,
		log:       logger,
		rtm:       rtm,
		events:    make(chan slack.RTMEvent),
		chanInfo:  make(map[string]*slack.Channel),
		usersInfo: make(map[string]*slack.User),
		invited:   make([]string, 0),
		online:    true,
	}
	// Load conversations that should still be active
	updateThreshold := time.Now().Add(-(time.Duration(mom.config.SessionTimeout) * time.Second))
	q := db.Where("name = ?", config.Name)
	q = q.Preload("BlacklistedUsers")
	q = q.Preload("Conversations", "updated_at > ?", updateThreshold, func(db *gorm.DB) *gorm.DB {
		return db.Order("conversations.direct_id desc, conversations.updated_at desc")
	})
	q = q.Preload("Conversations.MessageLogs")
	if err := q.FirstOrCreate(mom).Error; err != nil {
		mom.log.Fatal(err)
	}
	// If multiple Conversations per DirectID is loaded, only the most recent should be active
	i := 0
	var prev *Conversation
	for _, conv := range mom.Conversations {
		if prev != nil && conv.DirectID == prev.DirectID {
			continue
		}
		conv.init(mom)
		mom.Conversations[i] = conv
		i++
		prev = &conv
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
	mom.deactivateConversations(slackID)
	return true
}

func (mom *Mother) removeBlacklistedUser(slackID string) bool {
	for _, bu := range mom.BlacklistedUsers {
		if bu.SlackID == slackID {
			if err := db.Model(mom).Association("BlacklistedUsers").Delete(bu).Error; err != nil {
				mom.log.Println(err)
				return false
			}
			return true
		}
	}
	return false
}

func (mom *Mother) createConversation(directID string, slackIDs []string, notifyUsers bool) (*Conversation, error) {
	sort.Strings(slackIDs)
	tagged := make([]string, 0)
	for _, ID := range slackIDs {
		tagged = append(tagged, fmt.Sprintf("<@%s>", ID))
	}
	notice := fmt.Sprintf(mom.getMsg("sessionNotice"), strings.Join(tagged, ", "))
	threadID, err := mom.postMessage(mom.config.ChanID, "", notice)
	if err != nil {
		return nil, err
	}
	conv := &Conversation{
		MotherID: mom.ID,
		SlackIDs: strings.Join(slackIDs, ","),
		DirectID: directID,
		ThreadID: threadID,
	}
	conv.init(mom)
	if _, err := mom.trackConversation(conv); err != nil {
		if _, _, err := mom.rtm.DeleteMessage(mom.config.ChanID, threadID); err != nil {
			// In the worst case, this could result in an ugly situation where channel members are unknowingly sending
			// messages to an inactive thread, but the chances of this many things suddenly going wrong is extremely
			// unlikely
			mom.log.Println(err)
		}
		return nil, err
	}
	conv.sendMessageToThread(fmt.Sprintf(mom.getMsg("sessionStartConv"), conv.ThreadID))
	if notifyUsers {
		conv.sendMessageToDM(mom.getMsg("sessionStartDirect"))
	}
	return conv, nil
}

func (mom *Mother) loadConversation(threadID string) (*Conversation, error) {
	conv := &Conversation{}
	conv.init(mom)
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
	prev, err := conv.mom.trackConversation(conv);
	if err != nil {
		return nil, err
	}
	if !prev {
		conv.sendMessageToThread(conv.mom.getMsg("sessionResumeConv"))
		conv.sendMessageToDM(conv.mom.getMsg("sessionResumeDirect"))
	}
	return conv, nil
}

func (mom *Mother) trackConversation(conv *Conversation) (bool, error) {
	var prev *Conversation
	for i, p := range mom.Conversations {
		if mom.Conversations[i].active && mom.Conversations[i].DirectID == conv.DirectID {
			prev = &p
			mom.Conversations = append(mom.Conversations[:i], mom.Conversations[i+1:]...)
			break
		}
	}
	if err := db.Model(mom).Association("Conversations").Append(conv).Error; err != nil {
		return false, err
	}
	if prev != nil {
		link := mom.getMessageLink(conv.ThreadID)
		prev.sendMessageToThread(fmt.Sprintf(mom.getMsg("sessionContextSwitchedTo"), link))
		link = mom.getMessageLink(prev.ThreadID)
		conv.sendMessageToThread(fmt.Sprintf(mom.getMsg("sessionContextSwitchedFrom"), link))
	}
	return prev != nil, nil
}

func (mom *Mother) deactivateConversations(slackID string) {
	for i := range mom.Conversations {
		conv := &mom.Conversations[i]
		if !conv.active {
			continue
		}
		for _, ID := range strings.Split(conv.SlackIDs, ",") {
			if ID == slackID {
				conv.expire()
			}
		}
	}
	mom.reapConversations()
}

func (mom *Mother) reapConversations() {
	epoch := time.Now()
	i := 0
	for _, conv := range mom.Conversations {
		if conv.active && int64(epoch.Sub(conv.UpdatedAt).Seconds()) < mom.config.SessionTimeout {
			mom.Conversations[i] = conv
			i++
			continue
		}
		delete(mom.chanInfo, conv.DirectID)
		if conv.active {
			conv.expire()
		}
	}
	mom.Conversations = mom.Conversations[:i]
}

func (mom *Mother) findConversationByChannel(directID string) *Conversation {
	for _, conv := range mom.Conversations {
		if conv.active && conv.DirectID == directID {
			return &conv
		}
	}
	return nil
}

func (mom *Mother) findConversationByUsers(slackIDs []string) *Conversation {
	sort.Strings(slackIDs)
	seeking := strings.Join(slackIDs, ",")
	for i := range mom.Conversations {
		conv := &mom.Conversations[i]
		if conv.active && seeking == conv.SlackIDs {
			return conv
		}
	}
	return nil
}

func (mom *Mother) findConversationByTimestamp(timestamp string, loadExpired bool) *Conversation {
	for i := range mom.Conversations {
		conv := &mom.Conversations[i]
		if conv.active && conv.hasLog(timestamp) {
			return conv
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

func (mom *Mother) getChannelInfo(chanID string) (*slack.Channel, error) {
	if chanInfo, present := mom.chanInfo[chanID]; present {
		return chanInfo, nil
	}
	chanInfo, err := mom.rtm.GetConversationInfo(chanID, false)
	if err != nil {
		return nil, err
	}
	members, _, err := mom.rtm.GetUsersInConversation(
		&slack.GetUsersInConversationParameters{
			ChannelID: chanID,
			Cursor:    "",
			Limit:     0,
		},
	)
	if err != nil {
		return nil, err
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
	return chanInfo, nil
}

func (mom *Mother) getUserInfo(slackID string) (*slack.User, error) {
	if userInfo, present := mom.usersInfo[slackID]; present {
		return userInfo, nil
	}
	userInfo, err := mom.rtm.GetUserInfo(slackID)
	if err != nil {
		return nil, err
	}
	mom.usersInfo[slackID] = userInfo
	return userInfo, nil
}

func (mom *Mother) hasMember(slackID string) bool {
	chanInfo, err := mom.getChannelInfo(mom.config.ChanID)
	if err != nil {
		mom.log.Println(err)
		return false
	}
	for _, member := range chanInfo.Members {
		if member == slackID {
			return true
		}
	}
	return false
}

func (mom *Mother) isInvited(slackID string) bool {
	for _, invited := range mom.invited {
		if invited == slackID {
			return true
		}
	}
	return false
}

func (mom *Mother) inviteMember(slackID string) bool {
	if mom.isInvited(slackID) {
		return false
	}
	mom.invited = append(mom.invited, slackID)
	return true
}

func (mom *Mother) removeInvitation(slackID string) {
	for i, invited := range mom.invited {
		if invited == slackID {
			mom.invited = append(mom.invited[:i], mom.invited[i+1:]...)
			return
		}
	}
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

func (mom *Mother) runCommand(ev *slack.MessageEvent) {
	var reaction, threadID string
	args := strings.Split(ev.Text, " ")
	cmdName := strings.ToLower(args[0][1:])
	ref := slack.NewRefToMessage(ev.Channel, ev.Timestamp)
	cmd, present := commands[cmdName]
	if !present {
		if err := mom.rtm.AddReaction(mom.getMsg("reactUnknown"), ref); err != nil {
			mom.log.Println(err)
		}
		return
	}
	if ev.ThreadTimestamp == "" {
		threadID = ev.Timestamp
	} else {
		threadID = ev.ThreadTimestamp
	}
	success := cmd(
		mom,
		cmdParams{
			chanID:   ev.Channel,
			threadID: threadID,
			userID:   ev.User,
			args:     args[1:],
		},
	)
	if success {
		reaction = mom.getMsg("reactSuccess")
	} else {
		reaction = mom.getMsg("reactFailure")
	}
	if err := mom.rtm.AddReaction(reaction, ref); err != nil {
		mom.log.Println(err)
	}
}

func (mom *Mother) getMsg(key string) string {
	return mom.config.Lang[key]
}
