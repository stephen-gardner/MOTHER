package main

import (
	"encoding/json"
	"io/ioutil"
	"log"
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

func main() {
	botConfigs := loadConfig()
	openConnection()
	defer db.Close()
}
