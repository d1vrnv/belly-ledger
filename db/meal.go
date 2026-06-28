package db

import "time"

type Meal struct {
	ID          uint   `gorm:"primaryKey"`
	UserID      uint   `gorm:"not null;index"`
	User        User   `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`
	Description string `gorm:"type:text"`
	Healthiness string `gorm:"type:text"`
	Calories    int
	Protein     int
	Fats        int
	Carbs       int
	CreatedAt   time.Time
}

func (d *DB) GetCaloriesToday(userID uint) (int, error) {
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	end := start.Add(24 * time.Hour)

	var meals []Meal
	err := d.conn.
		Where("user_id = ? AND created_at >= ? AND created_at < ?", userID, start, end).
		Order("created_at asc").
		Find(&meals).Error

	if err != nil {
		return 0, err
	}
	var totalCalories int
	for _, m := range meals {
		totalCalories += m.Calories
	}
	return totalCalories, err
}

func (d *DB) AddMeal(userID uint, description, healthiness string, calories, protein, fats, carbs int) error {
	newMeal := &Meal{
		UserID:      userID,
		Description: description,
		Healthiness: healthiness,
		Calories:    calories,
		Protein:     protein,
		Fats:        fats,
		Carbs:       carbs,
	}
	return d.conn.Create(newMeal).Error
}
