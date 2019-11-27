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
	Token                  string
	ChanID                 string
	Enabled                bool
	AllowCommandsInChannel bool
	MaxFileSize            int
	SessionTimeout         int64
	TimeoutCheckInterval   int64
	ThreadsPerPage         int
	Lang                   map[string]string
}

var mothers = sync.Map{}

// This function should be called asynchronously
func blacklistBots(_, value interface{}) bool {
	mom := value.(*Mother)
	if !mom.isOnline() {
		return true
	}
	// Annoying Slackbot that we can't disable
	mom.events <- slack.RTMEvent{
		Type: "blacklist",
		Data: &blacklistEvent{Type: "blacklist", SlackID: "USLACKBOT"},
	}
	// Often it takes a moment for the bot to initialize and recognize its own identity
	for mom.rtm.GetInfo() == nil {
		time.Sleep(time.Second)
		if !mom.isOnline() {
			return true
		}
	}
	mothers.Range(func(_, value interface{}) bool {
		other := value.(*Mother)
		if !other.isOnline() {
			return true
		}
		for other.rtm.GetInfo() == nil {
			time.Sleep(time.Second)
			if !other.isOnline() {
				return true
			}
		}
		// Only blacklist bots located in the same workspace
		if other.rtm.GetInfo().Team.ID == mom.rtm.GetInfo().Team.ID {
			other.events <- slack.RTMEvent{
				Type: "blacklist",
				Data: &blacklistEvent{Type: "blacklist", SlackID: mom.rtm.GetInfo().User.ID},
			}
		}
		return true
	})
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
	mothers.Store(mom.Name, mom)
	mom.connect()
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
	go mothers.Range(blacklistBots)
	// Keep application alive until all bots are offline
	for {
		alive := false
		mothers.Range(func(_, value interface{}) bool {
			alive = true
			return false
		})
		if !alive {
			break
		}
		time.Sleep(10 * time.Second)
	}
}
