package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"time"

	"github.com/nlopes/slack"
)

type (
	botConfig struct {
		Name                 string
		Token                string
		ChanID               string
		Enabled              bool
		MaxFileSize          int
		SessionTimeout       int64
		TimeoutCheckInterval int64
		ThreadsPerPage       int
		Lang                 map[string]string
	}

	blacklistEvent struct {
		Type    string
		SlackID string
	}

	scrubEvent struct {
		Type string
	}
)

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

func handleEvents(mom *Mother, events <-chan slack.RTMEvent) {
	for msg := range events {
		switch ev := msg.Data.(type) {
		case *blacklistEvent:
			mom.blacklistUser(ev.SlackID)
		case *scrubEvent:
			mom.reapConversations()
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
	initCommands()
	openConnection()
	defer db.Close()

	for _, config := range botConfigs {
		if !config.Enabled {
			continue
		}
		mom := getMother(config)
		mothers = append(mothers, mom)
		go func(mom *Mother) {
			// To handle each bot's events synchronously
			go handleEvents(mom, mom.events)

			// Queues event to perform conversation reaping every TimeoutCheckInterval
			go func(mom *Mother) {
				for mom.online {
					mom.events <- slack.RTMEvent{
						Type: "scrub",
						Data: &scrubEvent{Type: "scrub"},
					}
					time.Sleep(time.Duration(mom.config.TimeoutCheckInterval) * time.Second)
				}
			}(mom)

			// Forwards events from Slack API library to allow us to mix in our own events
			for msg := range mom.rtm.IncomingEvents {
				if !mom.online {
					break
				}
				mom.events <- msg
			}
			close(mom.events)
		}(mom)
	}

	// We need the bots to blacklist each other to avoid potentially looping messages
	for _, mom := range mothers {
		go func(mom *Mother) {
			mom.events <- slack.RTMEvent{
				Type: "blacklist",
				Data: &blacklistEvent{Type: "blacklist", SlackID: "USLACKBOT"},
			}
			// Often it takes a moment for the bot to initialize and recognize its own identity
			for mom.rtm.GetInfo() == nil {
				time.Sleep(time.Second)
			}
			for _, other := range mothers {
				other.events <- slack.RTMEvent{
					Type: "blacklist",
					Data: &blacklistEvent{Type: "blacklist", SlackID: mom.rtm.GetInfo().User.ID},
				}
			}
		}(mom)
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
