package main

import (
	"belly-ledger/db"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
	log "github.com/sirupsen/logrus"
	genai "google.golang.org/genai"
	"gorm.io/gorm"
)

type GeminiResponse struct {
	FoodDescription        string `json:"food_description"`
	Calories               int    `json:"calories"`
	Protein                int    `json:"protein"`
	Fats                   int    `json:"fats"`
	Carbs                  int    `json:"carbs"`
	HealthinessExplanation string `json:"healthiness"`
}

type PendingReply struct {
	ChatID           int64
	UserID           int64
	ReplyToMessageID int
	Kind             string
}

func main() {
	loadEnv()
	var pendingReplies = map[int64]PendingReply{} // key: telegram user id

	database, err := db.Connect("belly.db")
	if err != nil {
		log.Fatal(err)
	}

	telegramToken := os.Getenv("TELEGRAMTOKEN")
	geminiKey := os.Getenv("GEMINIKEY")

	if telegramToken == "" {
		log.Fatal("TELEGRAMTOKEN is not set")
	}
	if geminiKey == "" {
		log.Fatal("GEMINIKEY is not set")
	}

	log.SetFormatter(&log.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: "2006-01-02 15:04:05",
		DisableSorting:  false,
		DisableQuote:    true,
	})

	if telegramToken == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN is not set")
	}
	if geminiKey == "" {
		log.Fatal("GEMINI_API_KEY is not set")
	}

	ctx := context.Background()

	aiClient, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  geminiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		log.Fatalf("failed to create Gemini client: %v", err)
	}

	bot, err := tgbotapi.NewBotAPI(telegramToken)
	if err != nil {
		log.Fatalf("failed to create Telegram bot: %v", err)
	}

	log.Printf("authorized on account %s", bot.Self.UserName)

	updateConfig := tgbotapi.NewUpdate(0)
	updateConfig.Timeout = 30
	updates := bot.GetUpdatesChan(updateConfig)

	for update := range updates {
		if update.CallbackQuery != nil {
			handleCallback(bot, database, update)
			continue
		}

		if update.Message == nil {
			continue
		}

		messageText := update.Message.Text
		chatID := update.Message.Chat.ID

		if messageText == "/start" {
			startMessage(bot, database, update, chatID, pendingReplies)
			continue
		}

		pending, hasPending := pendingReplies[update.Message.From.ID]
		if hasPending &&
			update.Message.ReplyToMessage != nil &&
			update.Message.ReplyToMessage.MessageID == pending.ReplyToMessageID &&
			pending.Kind == "user_profile" {
			err := processPendingReply(bot, database, update, pendingReplies)
			if err != nil {
				continue
			}
			continue
		}

		err := processMealRequest(bot, database, update, chatID, ctx, aiClient)
		if err != nil {
			continue
		}

	}
}

func loadEnv() {
	if err := godotenv.Load(); err != nil {
		log.Warning(".env not found, using system environment variables")
	}
}

func askGemini(ctx context.Context, client *genai.Client, prompt string, imgData []byte, mimeType string) (GeminiResponse, error) {
	var parts []*genai.Part

	if len(imgData) > 0 {
		parts = append(parts, genai.NewPartFromBytes(imgData, mimeType))
		if prompt != "" {
			parts = append(parts, genai.NewPartFromText(prompt))
		} else {
			parts = append(parts, genai.NewPartFromText("Describe what you see in this picture."))
		}
	} else {
		if prompt != "" {
			parts = append(parts, genai.NewPartFromText(prompt))
		} else {
			return GeminiResponse{}, fmt.Errorf("please provide a question or an image")
		}
	}

	contents := []*genai.Content{
		genai.NewContentFromParts(parts, genai.RoleUser),
	}

	systemPrompt := `You are a nutrition expert. Analyze the user's input (a food description or a photo of a meal) and estimate its nutritional content.
Return the analysis as a JSON object with the requested schema.`

	schema := &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"food_description": {Type: genai.TypeString},
			"calories":         {Type: genai.TypeInteger},
			"protein":          {Type: genai.TypeInteger},
			"fats":             {Type: genai.TypeInteger},
			"carbs":            {Type: genai.TypeInteger},
			"healthiness":      {Type: genai.TypeString},
		},
		Required: []string{"food_description", "calories", "protein", "fats", "carbs", "healthiness"},
	}

	config := &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText(systemPrompt, genai.RoleUser),
		ResponseMIMEType:  "application/json",
		ResponseSchema:    schema,
	}

	resp, err := client.Models.GenerateContent(ctx, "gemini-3.1-flash-lite", contents, config)
	if err != nil {
		return GeminiResponse{}, err
	}
	geminiMetadata := log.Fields{
		"CandidatesTokenCount":    resp.UsageMetadata.CandidatesTokenCount,
		"PromptTokenCount":        resp.UsageMetadata.PromptTokenCount,
		"ThoughtsTokenCount":      resp.UsageMetadata.ThoughtsTokenCount,
		"TotalTokenCount":         resp.UsageMetadata.TotalTokenCount,
		"ToolUsePromptTokenCount": resp.UsageMetadata.ToolUsePromptTokenCount,
		"ModelVersion":            resp.ModelVersion,
		"ResponseID":              resp.ResponseID,
	}
	log.WithFields(geminiMetadata).Info("Gemini response metadata")

	text := strings.TrimSpace(resp.Text())
	if text == "" {
		return GeminiResponse{}, fmt.Errorf("no response from Gemini")
	}

	cleanedText := cleanJSON(text)

	var result GeminiResponse
	if err := json.Unmarshal([]byte(cleanedText), &result); err != nil {
		return GeminiResponse{}, fmt.Errorf("failed to parse Gemini response: %w (raw response: %s)", err, text)
	}

	return result, nil
}

func cleanJSON(s string) string {
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start != -1 && end != -1 && end > start {
		return s[start : end+1]
	}
	return s
}

func bmiCategory(bmi float64) string {
	switch {
	case bmi < 18.5:
		return "underweight"
	case bmi < 25:
		return "normal"
	case bmi < 30:
		return "overweight"
	default:
		return "obese"
	}
}

func updateOrSendMessage(bot *tgbotapi.BotAPI, chatID int64, messageID int, text string) {
	if messageID != 0 {
		editMsg := tgbotapi.NewEditMessageText(chatID, messageID, text)
		if _, err := bot.Send(editMsg); err != nil {
			log.Errorf("failed to edit message: %v", err)
		}
	} else {
		msg := tgbotapi.NewMessage(chatID, text)
		if _, err := bot.Send(msg); err != nil {
			log.Errorf("failed to send message: %v", err)
		}
	}
}

func containsAlphanumeric(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

func handleUserProfileReply(bot *tgbotapi.BotAPI, database *db.DB, update tgbotapi.Update) error {
	parts := strings.Fields(strings.TrimSpace(strings.ToLower(update.Message.Text)))
	if len(parts) != 4 {
		return fmt.Errorf("invalid format")
	}

	height, err := strconv.Atoi(parts[0])
	if err != nil {
		return err
	}

	weight, err := strconv.Atoi(parts[1])
	if err != nil {
		return err
	}

	age, err := strconv.Atoi(parts[2])
	if err != nil {
		return err
	}

	gender := parts[3]
	if strings.ToLower(gender) != "m" && strings.ToLower(gender) != "f" {
		return fmt.Errorf("invalid gender")
	}
	user, err := database.GetUserByTelegramID(update.Message.From.ID)
	var newUser *db.User

	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			newUser, err = database.AddUser(update.Message.From.ID, update.Message.From.FirstName, height, weight, age, gender)
			if err != nil {
				return err
			}
		} else {
			return err
		}
	} else {
		if err := database.UpdateUserData(update.Message.From.ID, height, weight, age, gender); err != nil {
			return err
		}
		_ = user
	}

	genderLine := "♂️ Male"
	if gender == "f" {
		genderLine = "♀️ Female"
	}
	text := fmt.Sprintf(
		"✅ Profile saved\n"+
			"📏 %d cm\n"+
			"⚖️ %d kg\n"+
			"🎂 %d years\n"+
			"%s\n"+
			"BMI: %.1f (%s)\n"+
			"📐 Calculation using Mifflin-St Jeor formula",
		height,
		weight,
		age,
		genderLine,
		newUser.BMI,
		bmiCategory(newUser.BMI),
	)

	msg := tgbotapi.NewMessage(update.Message.Chat.ID, text)
	_, _ = bot.Send(msg)

	return nil
}

func startMessage(bot *tgbotapi.BotAPI, database *db.DB, update tgbotapi.Update, chatID int64, pendingReplies map[int64]PendingReply) {
	msg := tgbotapi.NewMessage(chatID, "Hi! I'm FoodBot - your personal nutrition assistant.\n\nI accept both food photos and text meal descriptions.\n\n📸 Send me a food photo or describe what you ate, and I will:\n• Identify composition and calories\n• Save data to your profile\n• Give recommendations")
	if _, err := bot.Send(msg); err != nil {
		log.Println("send welcome error:", err)
	}

	_, err := database.GetUserByTelegramID(update.Message.From.ID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			text := "Send height, weight, age and gender (m/f) in one message.\n\n" +
				"Examples:\n" +
				"180 75 25 m\n" +
				"165 60 30 f\n\n" +
				"ℹ️ Age and gender are needed for accurate calorie calculation using Mifflin-St Jeor formula. Without them, a simplified calculation is used."

			replyMsg := tgbotapi.NewMessage(chatID, text)
			replyMsg.ReplyMarkup = tgbotapi.ForceReply{
				ForceReply:            true,
				InputFieldPlaceholder: "180 75 25 m",
				Selective:             true,
			}
			replyMsg.ReplyToMessageID = update.Message.MessageID

			sent, sendErr := bot.Send(replyMsg)

			if sendErr == nil {
				pendingReplies[update.Message.From.ID] = PendingReply{
					ChatID:           chatID,
					UserID:           update.Message.From.ID,
					ReplyToMessageID: sent.MessageID,
					Kind:             "user_profile",
				}
			} else {
				log.Warning("send welcome error:", sendErr)
			}

		} else {
			errMsg := tgbotapi.NewMessage(chatID, "Sorry, I couldn't check your profile right now.")
			bot.Send(errMsg)
		}
	}
}

func processPendingReply(bot *tgbotapi.BotAPI, database *db.DB, update tgbotapi.Update, pendingReplies map[int64]PendingReply) error {
	err := handleUserProfileReply(bot, database, update)
	if err != nil {
		msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Couldn't parse that. Use format: 180 75 25 m")
		bot.Send(msg)
		return err
	}

	delete(pendingReplies, update.Message.From.ID)
	return nil
}

func processMealRequest(bot *tgbotapi.BotAPI, database *db.DB, update tgbotapi.Update, chatID int64, ctx context.Context, aiClient *genai.Client) error {
	var fileID string
	var mimeType string
	var prompt string
	var unsupportedType string

	user, err := database.GetUserByTelegramID(chatID)
	if err != nil {
		bot.Send(tgbotapi.NewMessage(update.Message.Chat.ID, "You need register your profile first /start"))
		return nil
	}
	if update.Message.Sticker != nil {
		unsupportedType = "stickers"
	} else if len(update.Message.Photo) > 0 {
		// Get largest photo size
		fileID = update.Message.Photo[len(update.Message.Photo)-1].FileID
		mimeType = "image/jpeg"
		prompt = update.Message.Caption
	} else if update.Message.Document != nil && strings.HasPrefix(update.Message.Document.MimeType, "image/") {
		fileID = update.Message.Document.FileID
		mimeType = update.Message.Document.MimeType
		prompt = update.Message.Caption
	} else if update.Message.Text != "" {
		prompt = update.Message.Text
	} else if update.Message.Voice != nil || update.Message.Audio != nil {
		unsupportedType = "audio/voice messages"
	} else if update.Message.Video != nil || update.Message.VideoNote != nil {
		unsupportedType = "video messages"
	} else if update.Message.Animation != nil {
		unsupportedType = "animations/GIFs"
	} else if update.Message.Location != nil {
		unsupportedType = "locations"
	} else if update.Message.Contact != nil {
		unsupportedType = "contacts"
	} else {
		unsupportedType = "this type of message"
	}

	// If it's a text-only message, check if it doesn't contain alphanumeric characters (e.g. only emojis/symbols)
	if fileID == "" && unsupportedType == "" && !containsAlphanumeric(prompt) {
		unsupportedType = "emoji-only or symbol-only messages"
	}

	if unsupportedType != "" {
		msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("I cannot process %s.", unsupportedType))
		if _, err := bot.Send(msg); err != nil {
			log.Warningf("failed to send message: %v", err)
		}
	}

	// Send initial processing message
	processingMsg, sendErr := bot.Send(tgbotapi.NewMessage(chatID, "Processing your request..."))
	if sendErr != nil {
		log.Warningf("failed to send processing message: %v", sendErr)
	}

	var imgData []byte
	if fileID != "" {
		// Download image from Telegram
		fileURL, err := bot.GetFileDirectURL(fileID)
		if err != nil {
			log.Warningf("failed to get file direct URL: %v", err)
			updateOrSendMessage(bot, chatID, processingMsg.MessageID, "Error getting image URL from Telegram.")
			return err
		}

		resp, err := http.Get(fileURL)
		if err != nil {
			log.Warningf("failed to download image: %v", err)
			updateOrSendMessage(bot, chatID, processingMsg.MessageID, "Error downloading image.")
			return err
		}
		defer resp.Body.Close()

		imgData, err = io.ReadAll(resp.Body)
		if err != nil {
			log.Warningf("failed to read image bytes: %v", err)
			updateOrSendMessage(bot, chatID, processingMsg.MessageID, "Error reading image data.")
			return err
		}
	}

	result, err := askGemini(ctx, aiClient, prompt, imgData, mimeType)

	var replyText string
	if err != nil {
		replyText = fmt.Sprintf("Error: %v", err)
		updateOrSendMessage(bot, chatID, processingMsg.MessageID, replyText)
	} else {
		meal, err := database.AddMeal(user.ID, result.FoodDescription, result.HealthinessExplanation, result.Calories, result.Protein, result.Fats, result.Carbs, update.Message.MessageID)
		if err != nil {
			log.Errorf("Processing meal error: %v", err)
		}

		totalCalories, err := database.GetCaloriesToday(user.ID)
		if err != nil {
			log.Errorf("Get calories today error: %v", err)
		}

		replyText = fmt.Sprintf("⏰ %s\n📝 %s\n🔥 %d kcal | Protein %dg | Fats %dg | Carbs %dg\n\n\nHealthiness: %s\n\nDaily target: %d/%d kcal",
			time.Now().Format("15:04"),
			result.FoodDescription,
			result.Calories, result.Protein, result.Fats, result.Carbs,
			result.HealthinessExplanation,
			totalCalories,
			user.Goal,
		)
		err = sendAnalyticsResponse(bot, chatID, processingMsg.MessageID, replyText, meal.ID)
		if err != nil {
			return err
		}
	}
	return nil
}

func sendAnalyticsResponse(
	bot *tgbotapi.BotAPI,
	chatID int64,
	messageID int,
	replyText string,
	mealID uint,
) error {
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(
				"🗑 Delete this entry",
				fmt.Sprintf("delete_meal:%d", mealID),
			),
		),
	)

	msg := tgbotapi.NewEditMessageText(chatID, messageID, replyText)
	msg.ReplyMarkup = &keyboard

	_, err := bot.Send(msg)
	return err
}

func handleCallback(bot *tgbotapi.BotAPI, database *db.DB, update tgbotapi.Update) {
	if update.CallbackQuery == nil {
		return
	}

	q := update.CallbackQuery
	if _, err := bot.Request(tgbotapi.NewCallback(q.ID, "")); err != nil {
		log.Println("answer callback failed:", err)
		return
	}
	if strings.HasPrefix(q.Data, "delete_meal:") {
		idStr := strings.TrimPrefix(q.Data, "delete_meal:")
		mealID, err := strconv.ParseUint(idStr, 10, 64)
		if err != nil {
			log.Println("parse meal id error:", err)
			return
		}

		meal, err := database.GetMealByID(uint(mealID))
		if err != nil {
			log.Println("get meal error:", err)
			return
		}

		if err := database.DeleteMeal(uint(mealID)); err != nil {
			log.Println("delete meal error:", err)
			return
		}

		_, err = bot.Request(tgbotapi.NewDeleteMessage(q.Message.Chat.ID, meal.SourceMessageID))
		if err != nil {
			log.Println("delete original photo error:", err)
		}

		_, err = bot.Request(tgbotapi.NewDeleteMessage(q.Message.Chat.ID, q.Message.MessageID))
		if err != nil {
			log.Println("delete telegram message error:", err)
		}
	}
}
