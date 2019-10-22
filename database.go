package main

import (
	"log"

	"github.com/jinzhu/gorm"
	_ "github.com/jinzhu/gorm/dialects/mysql"
)

var db *gorm.DB

func openConnection() {
	var err error
	if db, err = gorm.Open(
		"mysql",
		"root:root@(localhost:8889)/motherbot?charset=utf8mb4&parseTime=True&loc=Local",
	); err != nil {
		log.Fatal(err)
	}
	if err = db.AutoMigrate(
		&BlacklistedUser{},
		&Conversation{},
		&MessageLog{},
		&Mother{},
	).Error; err != nil {
		log.Fatal(err)
	}
}
