package main

import (
	"encoding/json"
	"io/ioutil"
	"log"
	"strconv"
	"time"

	"github.com/jinzhu/gorm"
	_ "github.com/jinzhu/gorm/dialects/mysql"
)

var db *gorm.DB

func openConnection() {
	var err error
	var data []byte
	if data, err = ioutil.ReadFile("database.json"); err == nil {
		config := make(map[string]string)
		if err = json.Unmarshal(data, &config); err == nil {
			if db, err = gorm.Open(config["driverName"], config["dataSource"]); err == nil {
				connMaxLifetime, _ := strconv.Atoi(config["connMaxLifetime"])
				maxIdleConns, _ := strconv.Atoi(config["maxIdleConns"])
				db.DB().SetConnMaxLifetime(time.Minute * time.Duration(connMaxLifetime))
				db.DB().SetMaxIdleConns(maxIdleConns)
				err = db.AutoMigrate(
					&BlacklistedUser{},
					&Conversation{},
					&MessageLog{},
					&Mother{},
				).Error
			}
		}
	}

	if err != nil {
		log.Fatal(err)
	}
}
