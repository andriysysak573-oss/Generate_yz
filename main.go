package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type BotConfig struct {
	Token      string
	AllowedID  int64 // 0 = дозволити всім, або вкажи свій Telegram ID
	Port       string
}

type SearchState struct {
	mu          sync.Mutex
	Running     bool
	Length      int
	Checked     int
	Found       int
	DelaySec    int
	Seen        map[string]bool
	StopChannel chan struct{}
}

type ScoreResult struct {
	Liquidity   int
	Readability int
	Tier        string
}

type CheckResult struct {
	Username       string
	TGFree         bool
	FragmentFree   bool
	FragmentStatus string
	Score          ScoreResult
}

var (
	vowels     = []string{"a", "e", "i", "o", "u", "y"}
	consonants = []string{
		"b", "c", "d", "f", "g", "h", "j", "k", "l",
		"m", "n", "p", "q", "r", "s", "t", "v", "w", "x", "z",
	}
	rareSet = map[string]bool{
		"q": true, "x": true, "z": true, "j": true, "w": true,
	}
)

func main() {
	rand.Seed(time.Now().UnixNano())

	token := strings.TrimSpace(os.Getenv("BOT_TOKEN"))
	if token == "" {
		log.Fatal("Не задано змінну оточення BOT_TOKEN")
	}

	var allowedID int64
	if rawID := strings.TrimSpace(os.Getenv("ALLOWED_CHAT_ID")); rawID != "" {
		parsed, err := strconv.ParseInt(rawID, 10, 64)
		if err == nil {
			allowedID = parsed
		}
	}

	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		port = "10000"
	}

	cfg := BotConfig{
		Token:     token,
		AllowedID: allowedID,
		Port:      port,
	}

	state := &SearchState{
		Running:  false,
		Length:   4,
		DelaySec: 4,
		Seen:     make(map[string]bool),
	}

	// HTTP-сервер для Render + UptimeRobot
	go startHTTPServer(cfg.Port, state)

	// Цикл опитування Telegram Bot API
	runBotPolling(cfg, state)
}

func startHTTPServer(port string, state *SearchState) {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		running := state.Running
		length := state.Length
		checked := state.Checked
		found := state.Found
		state.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  "ok",
			"running": running,
			"length":  length,
			"checked": checked,
			"found":   found,
		})
	})

	log.Printf("HTTP сервер слухає порт :%s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Printf("Помилка HTTP сервера: %v", err)
	}
}

func generateUsername(length int) string {
	mode := rand.Intn(4)
	var b strings.Builder

	switch mode {
	case 0:
		// Чергування приголосних і голосних
		for i := 0; i < length; i++ {
			if i%2 == 0 {
				b.WriteString(consonants[rand.Intn(len(consonants))])
			} else {
				b.WriteString(vowels[rand.Intn(len(vowels))])
			}
		}
	case 1:
		// Два приголосні на старті (як tkefy)
		pattern := []string{"c", "c", "v", "c"}
		if length == 5 {
			pattern = []string{"c", "c", "v", "c", "v"}
		}
		for _, p := range pattern {
			if p == "c" {
				b.WriteString(consonants[rand.Intn(len(consonants))])
			} else {
				b.WriteString(vowels[rand.Intn(len(vowels))])
			}
		}
	case 2:
		// Подвійна літера всередині
		baseLen := length - 1
		doubleAt := 1
		if baseLen > 2 {
			doubleAt = 1 + rand.Intn(baseLen-1)
		}
		for i := 0; i < baseLen; i++ {
			var ch string
			if i%2 == 0 {
				ch = consonants[rand.Intn(len(consonants))]
			} else {
				ch = vowels[rand.Intn(len(vowels))]
			}
			b.WriteString(ch)
			if i == doubleAt {
				b.WriteString(ch)
			}
		}
	default:
		// Збалансований випадковий з контролем серії приголосних
		streak := 0
		for i := 0; i < length; i++ {
			if streak >= 2 || rand.Float64() < 0.38 {
				b.WriteString(vowels[rand.Intn(len(vowels))])
				streak = 0
			} else {
				b.WriteString(consonants[rand.Intn(len(consonants))])
				streak++
			}
		}
	}

	res := b.String()
	if len(res) > length {
		return res[:length]
	}
	return res
}

func scoreUsername(u string) ScoreResult {
	u = strings.ToLower(strings.TrimSpace(u))
	readability := 58
	streak := 0
	vowelCount := 0
	prev := ""

	for i := 0; i < len(u); i++ {
		ch := string(u[i])
		isVowel := false
		for _, v := range vowels {
			if v == ch {
				isVowel = true
				break
			}
		}
		if isVowel {
			vowelCount++
			streak = 0
			readability += 5
		} else {
			streak++
			if streak >= 3 {
				readability -= 16
			} else if streak == 2 {
				readability -= 2
			}
		}
		if prev == ch {
			readability += 4
		}
		prev = ch
	}

	if vowelCount == 0 {
		readability -= 25
	}
	if vowelCount == len(u) && len(u) > 0 {
		readability -= 10
	}
	if readability < 5 {
		readability = 5
	}
	if readability > 100 {
		readability = 100
	}

	liquidity := 60
	if len(u) == 4 {
		liquidity = 76
	}
	liquidity += int(float64(readability-50) * 0.35)

	for i := 0; i < len(u); i++ {
		if rareSet[string(u[i])] {
			liquidity -= 5
		}
	}
	if liquidity < 5 {
		liquidity = 5
	}
	if liquidity > 100 {
		liquidity = 100
	}

	tier := "C"
	avg := (liquidity + readability) / 2
	switch {
	case avg >= 82 && len(u) == 4:
		tier = "S"
	case avg >= 72:
		tier = "A"
	case avg >= 55:
		tier = "B"
	}

	return ScoreResult{
		Liquidity:   liquidity,
		Readability: readability,
		Tier:        tier,
	}
}

func fetchBody(url string) (string, error) {
	client := &http.Client{Timeout: 8 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func checkUsernameAll(username string) CheckResult {
	clean := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(username, "@")))
	sc := scoreUsername(clean)

	tgFree := false
	if html, err := fetchBody("https://t.me/" + clean); err == nil {
		hasTitle := strings.Contains(html, `class="tgme_page_title"`)
		hasPreview := strings.Contains(html, "tgme_page_description") ||
			strings.Contains(html, "tgme_action_button_new") ||
			strings.Contains(html, "If you have <strong>Telegram</strong>")
		if !hasTitle && hasPreview && !strings.Contains(html, "tgme_page_extra") {
			tgFree = true
		}
	}

	fragFree := false
	fragStatus := "UNKNOWN"
	if html, err := fetchBody("https://fragment.com/username/" + clean); err == nil {
		lower := strings.ToLower(html)
		takenMarkers := []string{
			"on auction", "sold", "unavailable", "taken",
			"place bid", "buy username", "tm-auction",
		}
		taken := false
		for _, m := range takenMarkers {
			if strings.Contains(lower, m) {
				taken = true
				break
			}
		}
		if taken {
			fragFree = false
			fragStatus = "AUCTION/TAKEN"
		} else if strings.Contains(lower, "username is available") || strings.Contains(lower, "not assigned") {
			fragFree = true
			fragStatus = "FREE"
		} else {
			fragFree = false
			fragStatus = "CHECK_MANUALLY"
		}
	}

	return CheckResult{
		Username:       clean,
		TGFree:         tgFree,
		FragmentFree:   fragFree,
		FragmentStatus: fragStatus,
		Score:          sc,
	}
}

func startAutoSearch(token string, chatID int64, state *SearchState) {
	state.mu.Lock()
	if state.Running {
		state.mu.Unlock()
		sendMessage(token, chatID, "⚠️ Автопошук уже запущений.")
		return
	}
	state.Running = true
	stopCh := make(chan struct{})
	state.StopChannel = stopCh
	length := state.Length
	delay := time.Duration(state.DelaySec) * time.Second
	state.mu.Unlock()

	sendMessage(token, chatID, fmt.Sprintf("🚀 Автопошук запущено! Довжина: <b>%d</b> знаків. Чекаю вільні ніки...", length))

	go func() {
		for {
			select {
			case <-stopCh:
				sendMessage(token, chatID, "🛑 Автопошук зупинено.")
				return
			default:
			}

			state.mu.Lock()
			curLen := state.Length
			var candidate string
			for tries := 0; tries < 50; tries++ {
				cand := generateUsername(curLen)
				if !state.Seen[cand] {
					state.Seen[cand] = true
					candidate = cand
					break
				}
			}
			state.Checked++
			checkedNum := state.Checked
			state.mu.Unlock()

			if candidate != "" {
				res := checkUsernameAll(candidate)
				if res.TGFree && res.FragmentFree {
					state.mu.Lock()
					state.Found++
					foundNum := state.Found
					state.mu.Unlock()

					msg := fmt.Sprintf(
						"🔥 <b>ЗНАЙДЕНО #%d</b> (@%s)\n"+
							"• Довжина: <b>%d</b> | Tier: <b>%s</b>\n"+
							"• Ліквідність: <b>%d/100</b> | Читабельність: <b>%d/100</b>\n"+
							"• Telegram: <b>%v</b> | Fragment: <b>%s</b>\n"+
							"• Перевірено всього: %d\n\n"+
							"🔗 <a href=\"https://t.me/%s\">Відкрити в TG</a> | <a href=\"https://fragment.com/username/%s\">Fragment</a>",
						foundNum, res.Username, len(res.Username), res.Score.Tier,
						res.Score.Liquidity, res.Score.Readability,
						res.TGFree, res.FragmentStatus, checkedNum,
						res.Username, res.Username,
					)
					sendMessage(token, chatID, msg)
				}
			}

			select {
			case <-stopCh:
				sendMessage(token, chatID, "🛑 Автопошук зупинено.")
				return
			case <-time.After(delay):
			}
		}
	}()
}

func stopAutoSearch(token string, chatID int64, state *SearchState) {
	state.mu.Lock()
	if !state.Running {
		state.mu.Unlock()
		sendMessage(token, chatID, "ℹ️ Автопошук і так вимкнений.")
		return
	}
	state.Running = false
	close(state.StopChannel)
	state.mu.Unlock()
}

func sendMessage(token string, chatID int64, text string) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	payload := map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
		"reply_markup": map[string]any{
			"keyboard": [][]map[string]string{
				{{"text": "▶️ Старт 4 букви"}, {"text": "▶️ Старт 5 букв"}},
				{{"text": "⏹ Стоп"}, {"text": "📊 Статус"}},
			},
			"resize_keyboard": true,
		},
	}

	raw, _ := json.Marshal(payload)
	resp, err := http.Post(url, "application/json", bytes.NewReader(raw))
	if err != nil {
		log.Printf("Помилка надсилання повідомлення: %v", err)
		return
	}
	defer resp.Body.Close()
}

type tgUpdate struct {
	UpdateID int `json:"update_id"`
	Message  *struct {
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		Text string `json:"text"`
	} `json:"message"`
}

type tgResponse struct {
	OK     bool       `json:"ok"`
	Result []tgUpdate `json:"result"`
}

func runBotPolling(cfg BotConfig, state *SearchState) {
	offset := 0
	client := &http.Client{Timeout: 35 * time.Second}

	log.Println("Бот запущений та очікує повідомлення...")
	for {
		url := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates?offset=%d&timeout=25", cfg.Token, offset)
		resp, err := client.Get(url)
		if err != nil {
			time.Sleep(3 * time.Second)
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}

		var data tgResponse
		if err := json.Unmarshal(body, &data); err != nil || !data.OK {
			time.Sleep(2 * time.Second)
			continue
		}

		for _, upd := range data.Result {
			offset = upd.UpdateID + 1
			if upd.Message == nil {
				continue
			}

			chatID := upd.Message.Chat.ID
			if cfg.AllowedID != 0 && chatID != cfg.AllowedID {
				continue
			}

			text := strings.TrimSpace(upd.Message.Text)
			switch text {
			case "/start":
				msg := fmt.Sprintf(
					"👋 Привіт! Це бот автоперевірки 4- та 5-значних юзернеймів у <b>Telegram + Fragment</b>.\n\n"+
						"• Обери <b>Старт 4 букви</b> або <b>Старт 5 букв</b>\n"+
						"• Або просто надішли будь-який нік (наприклад <code>tkefy</code>) для ручної перевірки та оцінки ліквідності.\n\n"+
						"Твій Chat ID: <code>%d</code>",
					chatID,
				)
				sendMessage(cfg.Token, chatID, msg)

			case "▶️ Старт 4 букви", "/start4":
				state.mu.Lock()
				state.Length = 4
				state.mu.Unlock()
				startAutoSearch(cfg.Token, chatID, state)

			case "▶️ Старт 5 букв", "/start5":
				state.mu.Lock()
				state.Length = 5
				state.mu.Unlock()
				startAutoSearch(cfg.Token, chatID, state)

			case "⏹ Стоп", "/stop":
				stopAutoSearch(cfg.Token, chatID, state)

			case "📊 Статус", "/status":
				state.mu.Lock()
				msg := fmt.Sprintf(
					"📊 <b>Статус бота</b>\n"+
						"• Працює: <b>%v</b>\n"+
						"• Довжина: <b>%d</b>\n"+
						"• Перевірено: <b>%d</b>\n"+
						"• Знайдено: <b>%d</b>\n"+
						"• Інтервал: <b>%d сек</b>",
					state.Running, state.Length, state.Checked, state.Found, state.DelaySec,
				)
				state.mu.Unlock()
				sendMessage(cfg.Token, chatID, msg)

			default:
				if text != "" && !strings.HasPrefix(text, "/") {
					res := checkUsernameAll(text)
					msg := fmt.Sprintf(
						"🔍 <b>Перевірка @%s</b>\n"+
							"• Tier: <b>%s</b> | Ліквідність: <b>%d/100</b> | Читабельність: <b>%d/100</b>\n"+
							"• Telegram (вільний): <b>%v</b>\n"+
							"• Fragment: <b>%s</b>\n"+
							"🔗 <a href=\"https://t.me/%s\">TG</a> | <a href=\"https://fragment.com/username/%s\">Fragment</a>",
						res.Username, res.Score.Tier, res.Score.Liquidity, res.Score.Readability,
						res.TGFree, res.FragmentStatus, res.Username, res.Username,
					)
					sendMessage(cfg.Token, chatID, msg)
				}
			}
		}
	}
}
