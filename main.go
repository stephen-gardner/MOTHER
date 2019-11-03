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

type botConfig struct {
	Token                string
	ChanID               string
	Enabled              bool
	MaxFileSize          int
	SessionTimeout       int64
	TimeoutCheckInterval int64
	ThreadsPerPage       int
	Lang                 map[string]string
}

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

func loadBot(configFile os.FileInfo) bool {
	var config botConfig
	ext := filepath.Ext(configFile.Name())
	if configFile.IsDir() || ext != ".json" {
		return false
	}
	path := filepath.Join("bot_config", configFile.Name())
	data, err := ioutil.ReadFile(path)
	if err != nil {
		log.Println(err)
		return false
	}
	if err := json.Unmarshal(data, &config); err != nil {
		log.Println(err)
		return false
	}
	botName := strings.TrimSuffix(configFile.Name(), ext)
	if !config.Enabled {
		log.Println(botName, "is not enabled")
		return false
	}
	mom, err := getMother(botName, config)
	if err != nil {
		log.Println(err)
		return false
	}
	mom.connect()
	mothers.Store(mom.Name, mom)
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
		loaded := 0
		mothers.Range(func(_, value interface{}) bool {
			loaded++
			return true
		})
		if loaded == 0 {
			break
		}
		time.Sleep(10 * time.Second)
	}
}
