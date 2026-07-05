package db

import (
	"errors"
	"math"
	"time"
)

type User struct {
	ID         uint   `gorm:"primaryKey"`
	TelegramID int64  `gorm:"uniqueIndex;not null"`
	Name       string `gorm:"not null"`
	Height     int
	Weight     int
	Age        int
	Goal       int
	Gender     string
	BMI        float64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (d *DB) AddUser(telegramID int64, firstName string, height, weight, age int, gender string) (*User, error) {
	newUser := &User{
		TelegramID: telegramID,
		Name:       firstName,
		Height:     height,
		Weight:     weight,
		Age:        age,
		Gender:     gender,
		Goal:       calculateBMR(weight, height, age, gender),
		BMI:        calculateBMI(weight, height),
	}
	err := d.conn.Create(newUser).Error
	if err != nil {
		return nil, err
	}

	return newUser, nil
}

func (d *DB) GetUserByTelegramID(telegramID int64) (*User, error) {
	var user User

	err := d.conn.Where("telegram_id = ?", telegramID).First(&user).Error
	if err != nil {
		return nil, err
	}

	return &user, nil
}

func (d *DB) UpdateUserData(telegramID int64, height, weight int, age int, gender string) (*User, error) {
	result := d.conn.Model(&User{}).
		Where("telegram_id = ?", telegramID).
		Updates(map[string]interface{}{
			"height": height,
			"weight": weight,
			"age":    age,
			"gender": gender,
			"goal":   calculateBMR(weight, height, age, gender),
			"bmi":    calculateBMI(weight, height),
		})

	if result.Error != nil {
		return nil, result.Error
	}

	if result.RowsAffected == 0 {
		return nil, errors.New("user not found")
	}

	return d.GetUserByTelegramID(telegramID)
}

func calculateBMI(weightKg, heightCm int) float64 {
	if heightCm <= 0 {
		return 0
	}

	heightM := float64(heightCm) / 100.0
	bmi := float64(weightKg) / (heightM * heightM)

	return math.Round(bmi*10) / 10
}

func calculateBMR(weightKg, heightCm, age int, gender string) int {
	base := 10*float64(weightKg) + 6.25*float64(heightCm) - 5*float64(age)

	switch gender {
	case "m":
		return int(base + 5)
	case "f":
		return int(base - 161)
	default:
		return 0
	}
}
