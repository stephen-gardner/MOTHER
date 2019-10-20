package main

import (
	"encoding/json"
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
		case *slack.ConnectedEvent:
			mom.log.Println("Infos:", ev.Info)
			mom.log.Println("Connection counter:", ev.ConnectionCount)
		case *slack.RateLimitedError:
			mom.log.Println("Hitting RTM rate limit")
			time.Sleep(ev.RetryAfter * time.Second)
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
		go func(mom *Mother) {
			mom.blacklistUser("USLACKBOT")
			// It takes a moment for the bot to initialize and recognize its own identity
			for mom.rtm.GetInfo() == nil {
				time.Sleep(time.Second)
			}
			for _, other := range mothers {
				other.blacklistUser(mom.rtm.GetInfo().User.ID)
			}
		}(mom)
	}

	for online := true; online; {
		online = false
		for _, mom := range mothers {
			if mom.online {
				online = true
			}
		}
		time.Sleep(time.Minute)
	}
}
