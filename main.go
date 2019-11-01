package main

import (
	"encoding/json"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

var mothers = sync.Map{}

func blacklistBots(_, value interface{}) bool {
	mom := value.(*Mother)
	if !mom.online {
		return true
	}
	go func(mom *Mother) {
		// Annoying Slackbot that we can't disable
		mom.events <- slack.RTMEvent{
			Type: "blacklist",
			Data: &blacklistEvent{Type: "blacklist", SlackID: "USLACKBOT"},
		}
		// Often it takes a moment for the bot to initialize and recognize its own identity
		for mom.rtm.GetInfo() == nil {
			if !mom.online {
				return
			}
			time.Sleep(time.Second)
		}
		mothers.Range(func(_, value interface{}) bool {
			other := value.(*Mother)
			if !other.online {
				return true
			}
			go func(mom, other *Mother) {
				for other.rtm.GetInfo() == nil {
					if !other.online {
						return
					}
					time.Sleep(time.Second)
				}
				// Only blacklist bots located in the same workspace
				if other.rtm.GetInfo().Team.ID != mom.rtm.GetInfo().Team.ID {
					return
				}
				other.events <- slack.RTMEvent{
					Type: "blacklist",
					Data: &blacklistEvent{Type: "blacklist", SlackID: mom.rtm.GetInfo().User.ID},
				}
			}(mom, other)
			return true
		})
	}(mom)
	return true
}

func handleEvents(mom *Mother, events <-chan slack.RTMEvent) {
	for msg := range events {
		switch ev := msg.Data.(type) {
		case *blacklistEvent:
			mom.blacklistUser(ev.SlackID)

		case *scrubEvent:
			mom.reapConversations()
			mom.spoofAvailability()

		case *slack.ChannelJoinedEvent:
			handleChannelJoinedEvent(mom, ev)

		case *slack.ConnectedEvent:
			mom.log.Printf("Infos: %+v\n", *ev.Info)
			mom.log.Println("Connection counter:", ev.ConnectionCount)

		case *slack.DisconnectedEvent:
			mom.log.Println("Disconnected...")
			mom.online = false

		case *slack.GroupJoinedEvent:
			handleGroupJoinedEvent(mom, ev)

		case *slack.InvalidAuthEvent:
			mom.log.Println("Invalid credentials")
			mom.online = false
			return

		case *slack.MemberJoinedChannelEvent:
			handleMemberJoinedChannelEvent(mom, ev)

		case *slack.MemberLeftChannelEvent:
			handleMemberLeftChannelEvent(mom, ev)

		case *slack.MessageEvent:
			handleMessageEvent(mom, ev)

		case *slack.RateLimitedError:
			mom.log.Printf("Hitting RTM rate limit; sleeping for %d seconds\n", ev.RetryAfter)
			time.Sleep(ev.RetryAfter * time.Second)

		case *slack.ReactionAddedEvent:
			handleReactionAddedEvent(mom, ev)

		case *slack.ReactionRemovedEvent:
			handleReactionRemovedEvent(mom, ev)

		case *slack.RTMError:
			mom.log.Println("Error:", ev.Error())

		case *slack.UserTypingEvent:
			handleUserTypingEvent(mom, ev)

		default:
			// Ignore other events..
		}
	}
}

func loadBot(configFile os.FileInfo) bool {
	var config botConfig
	ext := filepath.Ext(configFile.Name())
	if configFile.IsDir() || ext != ".json" {
		return false
	}
	path := filepath.Join("bot_config", configFile.Name())
	data, err := ioutil.ReadFile(path)
	if err != nil {
		log.Fatal(err)
	}
	if err := json.Unmarshal(data, &config); err != nil {
		log.Fatal(err)
	}
	config.Name = strings.TrimSuffix(configFile.Name(), ext)
	if !config.Enabled {
		log.Println(config.Name, "is not enabled")
		return false
	}
	mom := getMother(config)
	mothers.Store(config.Name, mom)
	go func(mom *Mother) {
		defer close(mom.events)
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
	}(mom)
	return true
}

func main() {
	initCommands()
	openConnection()
	defer db.Close()
	// Attempt to load all json configuration files in "./bot_config"
	files, err := ioutil.ReadDir("bot_config")
	if err != nil {
		log.Fatal(err)
	}
	for _, configFile := range files {
		loadBot(configFile)
	}
	// We need the bots to blacklist each other to avoid potentially looping messages
	mothers.Range(blacklistBots)
	// Keep application alive until all bots are offline
	for {
		alive := 0
		mothers.Range(func(_, value interface{}) bool {
			mom := value.(*Mother)
			if mom.online {
				alive++
			}
			return true
		})
		if alive == 0 {
			break
		}
		time.Sleep(10 * time.Second)
	}
}
