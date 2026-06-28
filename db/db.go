package db

import (
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type DB struct {
	conn *gorm.DB
}

func Connect(path string) (*DB, error) {
	conn, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		return nil, err
	}

	if err := conn.AutoMigrate(&User{}, &Meal{}); err != nil {
		return nil, err
	}

	return &DB{conn: conn}, nil
}
