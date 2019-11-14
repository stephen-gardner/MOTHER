package main

import (
	"fmt"
	"log"
	"os"
	"regexp"
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
		chanInfo         map[string]expirable `gorm:"-"`
		usersInfo        map[string]expirable `gorm:"-"`
		invited          []string             `gorm:"-"`
		config           botConfig            `gorm:"-"`
		log              *log.Logger          `gorm:"-"`
		rtm              *slack.RTM           `gorm:"-"`
		events           chan slack.RTMEvent  `gorm:"-"`
		shutdown         chan struct{}        `gorm:"-"`
		connectedAt      time.Time            `gorm:"-"`
		reload           bool                 `gorm:"-"`
	}

	BlacklistedUser struct {
		gorm.Model
		MotherID uint
		SlackID  string
	}

	expirable struct {
		data      interface{}
		updatedAt time.Time
	}
)

func getMother(botName string, config botConfig) (*Mother, error) {
	mom := &Mother{
		Name:      botName,
		config:    config,
		log:       log.New(os.Stdout, botName+": ", log.LstdFlags),
		chanInfo:  make(map[string]expirable),
		usersInfo: make(map[string]expirable),
		invited:   make([]string, 0),
		reload:    false,
	}
	// Load conversations that should still be active
	updateThreshold := time.Now().Add(-(time.Duration(mom.config.SessionTimeout) * time.Second))
	err := db.
		Where("name = ?", mom.Name).
		Preload("BlacklistedUsers").
		Preload("Conversations", "active = ? AND updated_at > ?", true, updateThreshold,
			func(db *gorm.DB) *gorm.DB {
				return db.Order("conversations.direct_id desc, conversations.updated_at desc")
			},
		).
		Preload("Conversations.MessageLogs").
		FirstOrCreate(mom).Error
	if err != nil {
		return nil, err
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
	mom.Conversations = mom.Conversations[:i]
	return mom, nil
}

func (mom *Mother) connect() {
	mom.shutdown = make(chan struct{})
	mom.rtm = slack.New(mom.config.Token, slack.OptionDebug(false), slack.OptionLog(mom.log)).NewRTM()
	go mom.rtm.ManageConnection()
	go func(mom *Mother) {
		// To handle each bot's events synchronously
		mom.events = make(chan slack.RTMEvent)
		defer close(mom.events)
		go handleEvents(mom)
		scrubTicker := time.NewTicker(time.Duration(mom.config.TimeoutCheckInterval) * time.Second)
		for {
			select {
			// Forwards events from Slack API library to allow us to mix in our own events
			case msg := <-mom.rtm.IncomingEvents:
				mom.events <- msg
			// Queues scrub event every TimeoutCheckInterval
			case <-scrubTicker.C:
				mom.events <- slack.RTMEvent{
					Type: "scrub",
					Data: &scrubEvent{Type: "scrub"},
				}
			case <-mom.shutdown:
				return
			}
		}
	}(mom)
}

func (mom *Mother) isOnline() bool {
	select {
	case <-mom.shutdown:
		return false
	default:
		return true
	}
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
	err := db.
		Model(mom).
		Association("BlacklistedUsers").
		Append(bu).Error
	if err != nil {
		mom.log.Println(err)
		return false
	}
	mom.deactivateConversations(slackID)
	return true
}

func (mom *Mother) removeBlacklistedUser(slackID string) bool {
	for _, bu := range mom.BlacklistedUsers {
		if bu.SlackID == slackID {
			err := db.
				Model(mom).
				Association("BlacklistedUsers").
				Delete(bu).Error
			if err != nil {
				mom.log.Println(err)
				return false
			}
			return true
		}
	}
	return false
}

func (mom *Mother) deactivateConversations(slackID string) {
	for i := range mom.Conversations {
		conv := &mom.Conversations[i]
		if !conv.Active {
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
		if conv.Active && int64(epoch.Sub(conv.UpdatedAt).Seconds()) < mom.config.SessionTimeout {
			mom.Conversations[i] = conv
			i++
			continue
		}
		if conv.Active {
			conv.expire()
		}
	}
	mom.Conversations = mom.Conversations[:i]
}

func (mom *Mother) findConversationByChannel(directID string) *Conversation {
	for _, conv := range mom.Conversations {
		if conv.Active && conv.DirectID == directID {
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
		if conv.Active && seeking == conv.SlackIDs {
			return conv
		}
	}
	return nil
}

func (mom *Mother) findConversationByTimestamp(timestamp string, loadExpired bool) *Conversation {
	for i := range mom.Conversations {
		conv := &mom.Conversations[i]
		if conv.Active && conv.hasLog(timestamp) {
			return conv
		}
	}
	if !loadExpired {
		return nil
	}
	conv, err := mom.
		newConversation().
		loadConversation(timestamp).
		create()
	if err != nil {
		if err != gorm.ErrRecordNotFound && err != ErrUserNotAllowed {
			mom.log.Println(err)
		}
		return nil
	}
	return conv
}

func (mom *Mother) getChannelInfo(chanID string) (*slack.Channel, error) {
	if chanInfo, present := mom.chanInfo[chanID]; present {
		return chanInfo.data.(*slack.Channel), nil
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
	mom.chanInfo[chanID] = expirable{data: chanInfo, updatedAt: time.Now()}
	return chanInfo, nil
}

func (mom *Mother) getUserInfo(slackID string) (*slack.User, error) {
	if userInfo, present := mom.usersInfo[slackID]; present {
		return userInfo.data.(*slack.User), nil
	}
	info, err := mom.rtm.GetUserInfo(slackID)
	if err == nil {
		mom.usersInfo[slackID] = expirable{data: info, updatedAt: time.Now()}
	}
	return info, err
}

func (mom *Mother) pruneExpired(dataMap map[string]expirable) {
	for key, data := range dataMap {
		if int64(time.Now().Sub(data.updatedAt).Seconds()) >= mom.config.SessionTimeout {
			delete(dataMap, key)
		}
	}
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

func (mom *Mother) runCommand(ev *slack.MessageEvent, sender *slack.User, forceThreading bool) {
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
	if ev.ThreadTimestamp == "" && forceThreading {
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
		mom.log.Printf("<%s> %s\n", sender.Profile.DisplayName, ev.Text)
	} else {
		reaction = mom.getMsg("reactFailure")
	}
	if err := mom.rtm.AddReaction(reaction, ref); err != nil {
		mom.log.Println(err)
	}
}

func (mom *Mother) spoofAvailability(dummyChanID *string) {
	if dummyChanID == nil {
		dummy, _, _, err := mom.rtm.OpenConversation(
			&slack.OpenConversationParameters{Users: []string{"USLACKBOT"}},
		)
		if err != nil {
			mom.log.Println(err)
			return
		}
		dummyChanID = &dummy.ID
	}
	mom.rtm.SendMessage(mom.rtm.NewTypingMessage(*dummyChanID))
}

func (mom *Mother) translateSlackIDs(msg string) string {
	rgx := regexp.MustCompile("<@(.*?)>")
	for {
		res := rgx.FindStringSubmatch(msg)
		if res == nil {
			break
		}
		user, err := mom.getUserInfo(res[1])
		if err != nil {
			mom.log.Println(err)
			break
		}
		translated := fmt.Sprintf("@%s[%s]", user.Profile.DisplayName, res[1])
		msg = strings.ReplaceAll(msg, res[0], translated)
	}
	return msg
}

func (mom *Mother) getMsg(key string) string {
	return mom.config.Lang[key]
}
