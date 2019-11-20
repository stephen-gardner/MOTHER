package main

import (
	"log"
	"time"

	"github.com/jinzhu/gorm"
	_ "github.com/jinzhu/gorm/dialects/mysql"
	//_ "github.com/jinzhu/gorm/dialects/postgres"
)

var db *gorm.DB

func openConnection() {
	var err error
	if db, err = gorm.Open(
		"mysql",
		"root:root@(localhost:8889)/motherbot?charset=utf8mb4&parseTime=True&loc=Local",
	); err == nil {
		db.DB().SetConnMaxLifetime(time.Minute * 15)
		db.DB().SetMaxIdleConns(0)
		err = db.AutoMigrate(
			&BlacklistedUser{},
			&Conversation{},
			&MessageLog{},
			&Mother{},
		).Error
	}
	if err != nil {
		log.Fatal(err)
	}
}
