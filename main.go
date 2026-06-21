package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
	genai "google.golang.org/genai"
)

const (
	TargetCalories = 2202
	TargetProtein  = 165
	TargetFats     = 73
	TargetCarbs    = 220
)

type DailyNutrition struct {
	Calories int
	Protein  int
	Fats     int
	Carbs    int
	Date     string
}

type GeminiResponse struct {
	FoodDescription        string `json:"food_description"`
	Calories               int    `json:"calories"`
	Protein                int    `json:"protein"`
	Fats                   int    `json:"fats"`
	Carbs                  int    `json:"carbs"`
	HealthinessExplanation string `json:"healthiness"`
}

var (
	nutritionMutex sync.Mutex
	userNutrition  = make(map[int64]*DailyNutrition)
)

func main() {
	loadEnv()
	telegramToken := os.Getenv("TELEGRAMTOKEN")
	geminiKey := os.Getenv("GEMINIKEY")

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
		if update.Message == nil {
			continue
		}

		var fileID string
		var mimeType string
		var prompt string
		var unsupportedType string
		chatID := update.Message.Chat.ID

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
				log.Printf("failed to send message: %v", err)
			}
			continue
		}

		if prompt == "/start" {
			msg := tgbotapi.NewMessage(chatID, "Hi! Send me a question or a photo, and I will ask Gemini.")
			bot.Send(msg)
			continue
		}

		// Send initial processing message
		processingMsg, sendErr := bot.Send(tgbotapi.NewMessage(chatID, "Processing your request..."))
		if sendErr != nil {
			log.Printf("failed to send processing message: %v", sendErr)
		}

		var imgData []byte
		if fileID != "" {
			// Download image from Telegram
			fileURL, err := bot.GetFileDirectURL(fileID)
			if err != nil {
				log.Printf("failed to get file direct URL: %v", err)
				updateOrSendMessage(bot, chatID, processingMsg.MessageID, "Error getting image URL from Telegram.")
				continue
			}

			resp, err := http.Get(fileURL)
			if err != nil {
				log.Printf("failed to download image: %v", err)
				updateOrSendMessage(bot, chatID, processingMsg.MessageID, "Error downloading image.")
				continue
			}
			defer resp.Body.Close()

			imgData, err = io.ReadAll(resp.Body)
			if err != nil {
				log.Printf("failed to read image bytes: %v", err)
				updateOrSendMessage(bot, chatID, processingMsg.MessageID, "Error reading image data.")
				continue
			}
		}

		result, err := askGemini(ctx, aiClient, prompt, imgData, mimeType)
		var replyText string
		if err != nil {
			replyText = fmt.Sprintf("Error: %v", err)
		} else {
			nutritionMutex.Lock()
			currentDate := time.Now().Format("2006-01-02")
			stats, exists := userNutrition[chatID]
			if !exists || stats.Date != currentDate {
				stats = &DailyNutrition{Date: currentDate}
				userNutrition[chatID] = stats
			}
			stats.Calories += result.Calories
			stats.Protein += result.Protein
			stats.Fats += result.Fats
			stats.Carbs += result.Carbs
			nutritionMutex.Unlock()

			percentage := int(float64(stats.Calories) / float64(TargetCalories) * 100.0)
			left := TargetCalories - stats.Calories
			if left < 0 {
				left = 0
			}

			replyText = fmt.Sprintf("⏰ %s\n📝 %s\n🔥 %d kcal | Protein %dg | Fats %dg | Carbs %dg\n\n\nHealthiness: %s\n\nDaily target: %d/%d kcal (%d%%), left %d kcal\nProtein: %d/%dg\nFats: %d/%dg\nCarbs: %d/%dg",
				time.Now().Format("15:04"),
				result.FoodDescription,
				result.Calories, result.Protein, result.Fats, result.Carbs,
				result.HealthinessExplanation,
				stats.Calories, TargetCalories, percentage, left,
				stats.Protein, TargetProtein,
				stats.Fats, TargetFats,
				stats.Carbs, TargetCarbs,
			)
		}

		updateOrSendMessage(bot, chatID, processingMsg.MessageID, replyText)
	}
}

func loadEnv() {
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
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

	resp, err := client.Models.GenerateContent(ctx, "gemini-3.5-flash", contents, config)
	if err != nil {
		return GeminiResponse{}, err
	}

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

func updateOrSendMessage(bot *tgbotapi.BotAPI, chatID int64, messageID int, text string) {
	if messageID != 0 {
		editMsg := tgbotapi.NewEditMessageText(chatID, messageID, text)
		if _, err := bot.Send(editMsg); err != nil {
			log.Printf("failed to edit message: %v", err)
		}
	} else {
		msg := tgbotapi.NewMessage(chatID, text)
		if _, err := bot.Send(msg); err != nil {
			log.Printf("failed to send message: %v", err)
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
