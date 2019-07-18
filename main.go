package main

import (
	"encoding/json"
	"fmt"
	"github.com/nlopes/slack"
	"io/ioutil"
	"log"
	"time"
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

type botEvent struct {
	mom *mother
	msg slack.RTMEvent
}

type blacklistEvent struct {
	Type string
	User string
}

type scrubEvent struct {
	Type string
}

func handleEvents(events <-chan botEvent) {
	for bot := range events {
		mom := bot.mom
		if !mom.online {
			continue
		}

		switch ev := bot.msg.Data.(type) {
		case *slack.MessageEvent:
			if ev.SubType == "message_replied" {
				break // Thread update events
			}
			edit := ev.SubType == "message_changed"
			if (edit && mom.isBlacklisted(ev.SubMessage.User)) || mom.isBlacklisted(ev.User) {
				break
			}

			chanInfo := mom.getChannelInfo(ev.Channel)
			if chanInfo == nil {
				break
			}

			if edit {
				handleMessageChangedEvent(mom, ev, chanInfo)
			} else if ev.Channel == mom.config.ChanID {
				handleChannelMessageEvent(mom, ev)
			} else if chanInfo.IsIM || chanInfo.IsMpIM {
				handleDirectMessageEvent(mom, ev, chanInfo)
			}

		case *slack.UserTypingEvent:
			handleUserTypingEvent(mom, ev)

		case *slack.ReactionAddedEvent:
			handleReactionAddedEvent(mom, ev)

		case *slack.ReactionRemovedEvent:
			handleReactionRemovedEvent(mom, ev)

		case *slack.ConnectedEvent:
			fmt.Println("Infos:", ev.Info)
			fmt.Println("Connection counter:", ev.ConnectionCount)

		case *slack.RateLimitedError:
			mom.log.Println("Hitting RTM rate limit")
			time.Sleep(ev.RetryAfter * time.Second)

		case *blacklistEvent:
			mom.blacklistUser(ev.User)

		case *scrubEvent:
			mom.reapConversations(mom.config.SessionTimeout)
			delete(mom.chanInfo, mom.config.ChanID)

		default:
			// Ignore other events..
		}
	}
}

func main() {
	var bc []botConfig

	data, err := ioutil.ReadFile("config.json")
	if err != nil {
		log.Fatal(err)
	}
	err = json.Unmarshal(data, &bc)

	if err := openConnection("mother.db"); err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	events := make(chan botEvent)
	defer close(events)

	mothers := make([]*mother, 0)
	for _, config := range bc {
		if !config.Enabled {
			continue
		}
		mom := newMother(config)
		mothers = append(mothers, mom)

		go func(mom *mother) {
			for msg := range mom.rtm.IncomingEvents {
				if !mom.online {
					break
				}
				events <- botEvent{
					mom: mom,
					msg: msg,
				}
			}
		}(mom)

		go func(mom *mother) {
			for mom.online {
				time.Sleep(time.Duration(int64(time.Second) * mom.config.TimeoutCheckInterval))
				events <- botEvent{
					mom: mom,
					msg: slack.RTMEvent{
						Type: "scrub",
						Data: &scrubEvent{
							Type: "scrub",
						},
					},
				}
			}
		}(mom)
	}

	// Builds blacklist of bots in action and automatically adds them
	for _, mom := range mothers {
		go func(mom *mother) {
			mom.blacklistUser("USLACKBOT")
			for mom.rtm.GetInfo() == nil {
				time.Sleep(time.Second)
			}
			for _, otherMom := range mothers {
				events <- botEvent{
					mom: otherMom,
					msg: slack.RTMEvent{
						Type: "blacklist",
						Data: &blacklistEvent{
							Type: "blacklist",
							User: mom.rtm.GetInfo().User.ID,
						},
					},
				}
			}
		}(mom)
	}

	handleEvents(events)
}
