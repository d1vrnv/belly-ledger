package db

import "time"

type Meal struct {
	ID              uint   `gorm:"primaryKey"`
	UserID          uint   `gorm:"not null;index"`
	User            User   `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`
	Description     string `gorm:"type:text"`
	Healthiness     string `gorm:"type:text"`
	Calories        int
	Protein         int
	Fats            int
	Carbs           int
	CreatedAt       time.Time
	SourceMessageID int // original Telegram request message
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

func (d *DB) AddMeal(userID uint, description, healthiness string, calories, protein, fats, carbs int, SourceMessageID int) (*Meal, error) {
	newMeal := &Meal{
		UserID:          userID,
		Description:     description,
		Healthiness:     healthiness,
		Calories:        calories,
		Protein:         protein,
		Fats:            fats,
		Carbs:           carbs,
		SourceMessageID: SourceMessageID,
	}

	if err := d.conn.Create(newMeal).Error; err != nil {
		return nil, err
	}

	return newMeal, nil
}

func (d *DB) DeleteMeal(mealId uint) error {
	err := d.conn.Delete(&Meal{}, mealId).Error
	if err != nil {
		return err
	}
	return nil
}

func (d *DB) GetMealByID(id uint) (*Meal, error) {
	var meal Meal

	if err := d.conn.First(&meal, id).Error; err != nil {
		return nil, err
	}

	return &meal, nil
}
