package main

// Prices in Brazilian reais. itch.io shows prices in the developer's
// currency (almost always US dollars); they are converted with a daily
// exchange rate (open.er-api.com, no key needed), cached in the data folder.
// Without a rate the original price is shown.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type fxRates struct {
	mu        sync.RWMutex
	Base      string             `json:"base"`
	Rates     map[string]float64 `json:"rates"` // units per 1 USD
	FetchedAt time.Time          `json:"fetched_at"`
}

var fx = &fxRates{}

func fxPath() string { return filepath.Join(dataDir(), "fx-rates.json") }

// startFX loads the cached rates and refreshes them in the background once
// a day.
func startFX() {
	if data, err := os.ReadFile(fxPath()); err == nil {
		fx.mu.Lock()
		_ = json.Unmarshal(data, fx)
		fx.mu.Unlock()
	}
	go func() {
		for {
			fx.mu.RLock()
			age := time.Since(fx.FetchedAt)
			fx.mu.RUnlock()
			if age > 24*time.Hour {
				if err := refreshFX(); err != nil {
					fmt.Fprintln(os.Stderr, "exchange rate:", err)
				}
			}
			time.Sleep(time.Hour)
		}
	}()
}

func refreshFX() error {
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Get("https://open.er-api.com/v6/latest/USD")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out struct {
		Result string             `json:"result"`
		Rates  map[string]float64 `json:"rates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	if out.Result != "success" || out.Rates["BRL"] <= 0 {
		return fmt.Errorf("unexpected answer from the exchange-rate service")
	}
	fx.mu.Lock()
	fx.Base, fx.Rates, fx.FetchedAt = "USD", out.Rates, time.Now()
	data, _ := json.Marshal(fx)
	fx.mu.Unlock()
	return os.WriteFile(fxPath(), data, 0o644)
}

// currencyOf guesses the currency of an itch.io price label.
func currencyOf(label string) string {
	up := strings.ToUpper(label)
	switch {
	case strings.Contains(up, "R$") || strings.Contains(up, "BRL"):
		return "BRL"
	case strings.Contains(label, "€") || strings.Contains(up, "EUR"):
		return "EUR"
	case strings.Contains(label, "£") || strings.Contains(up, "GBP"):
		return "GBP"
	case strings.Contains(label, "¥") || strings.Contains(up, "JPY"):
		return "JPY"
	case strings.Contains(up, "CAD"):
		return "CAD"
	case strings.Contains(up, "AUD"):
		return "AUD"
	}
	return "USD"
}

// toBRL converts an itch.io price label to reais. ok is false when no
// exchange rate is known yet.
func toBRL(label string) (float64, bool) {
	v := priceValue(label)
	cur := currencyOf(label)
	if cur == "BRL" {
		return v, true
	}
	fx.mu.RLock()
	defer fx.mu.RUnlock()
	brl, from := fx.Rates["BRL"], fx.Rates[cur]
	if cur == "USD" {
		from = 1
	}
	if brl <= 0 || from <= 0 {
		return 0, false
	}
	return v / from * brl, true
}

// formatBRL writes 16.5 as "R$ 16,50".
func formatBRL(v float64) string {
	s := fmt.Sprintf("%.2f", v)
	return "R$ " + strings.Replace(s, ".", ",", 1)
}

// priceInReais is the label shown to the user: "R$ 16,50", or the original
// price when no rate is available yet.
func priceInReais(label string) string {
	if v, ok := toBRL(label); ok {
		return formatBRL(v)
	}
	return label
}
