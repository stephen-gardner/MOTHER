package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"time"

	"github.com/nlopes/slack"
)

type botConfig struct {
	Name                 string
	Token                string
	ChanID               string
	Enabled              bool
	SessionTimeout       int64
	TimeoutCheckInterval int64
	ThreadsPerPage       int
	Lang                 map[string]string
}

func loadConfig() []botConfig {
	var bConfig []botConfig
	data, err := ioutil.ReadFile("config.json")
	if err != nil {
		log.Fatal(err)
	}
	if err := json.Unmarshal(data, &bConfig); err != nil {
		log.Fatal(err)
	}
	return bConfig
}

func handleEvents(mom *Mother) {
	for inc := range mom.rtm.IncomingEvents {
		switch ev := inc.Data.(type) {
		case *slack.ChannelJoinedEvent:
			handleChannelJoinedEvent(mom, ev)
		case *slack.ConnectedEvent:
			mom.log.Println("Infos:", ev.Info)
			mom.log.Println("Connection counter:", ev.ConnectionCount)
		case *slack.GroupJoinedEvent:
			handleGroupJoinedEvent(mom, ev)
		case *slack.MemberJoinedChannelEvent:
			handleMemberJoinedChannelEvent(mom, ev)
		case *slack.MemberLeftChannelEvent:
			handleMemberLeftChannelEvent(mom, ev)
		case *slack.MessageEvent:
			if ev.SubType == "message_replied" {
				break // Thread update events
			}
			edit := ev.SubType == "message_changed"
			if mom.isBlacklisted(ev.User) || (edit && mom.isBlacklisted(ev.SubMessage.User)) {
				break
			}
			chanInfo, err := mom.getChannelInfo(ev.Channel)
			if err != nil {
				mom.log.Println(err)
				break
			}
			if edit {
				handleMessageChangedEvent(mom, ev, chanInfo)
			} else if ev.Channel == mom.config.ChanID {
				handleChannelMessageEvent(mom, ev)
			} else if chanInfo.IsIM || chanInfo.IsMpIM {
				handleDirectMessageEvent(mom, ev, chanInfo)
			}
		case *slack.RateLimitedError:
			mom.log.Println(fmt.Sprintf("Hitting RTM rate limit; sleeping for %d seconds\n", ev.RetryAfter))
			time.Sleep(ev.RetryAfter * time.Second)
		case *slack.ReactionAddedEvent:
			handleReactionAddedEvent(mom, ev)
		case *slack.ReactionRemovedEvent:
			handleReactionRemovedEvent(mom, ev)
		case *slack.UserTypingEvent:
			handleUserTypingEvent(mom, ev)
		default:
			// Ignore other events..
		}
	}
}

func main() {
	mothers := make([]*Mother, 0)
	botConfigs := loadConfig()
	openConnection()
	defer db.Close()

	for _, config := range botConfigs {
		if !config.Enabled {
			continue
		}
		mom := getMother(config)
		mothers = append(mothers, mom)

		go func(mom *Mother) {
			handleEvents(mom)
		}(mom)

		go func(mom *Mother) {
			for mom.online {
				mom.reapConversations()
				time.Sleep(time.Duration(mom.config.TimeoutCheckInterval) * time.Second)
			}
		}(mom)
	}

	for _, mom := range mothers {
		mom.blacklistUser("USLACKBOT")
		// It takes a moment for the bot to initialize and recognize its own identity
		for mom.rtm.GetInfo() == nil {
			time.Sleep(time.Second)
		}
		for _, other := range mothers {
			other.blacklistUser(mom.rtm.GetInfo().User.ID)
		}
	}

	for online := true; online; {
		online = false
		time.Sleep(time.Minute)
		for _, mom := range mothers {
			if mom.online {
				online = true
			}
		}
	}
}
