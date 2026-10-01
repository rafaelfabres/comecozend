package main

import "testing"

func TestToBRL(t *testing.T) {
	fx.mu.Lock()
	fx.Rates = map[string]float64{"BRL": 5.5, "EUR": 0.9}
	fx.mu.Unlock()
	cases := map[string]string{"$3.00 USD": "R$ 16,50", "$4.99": "R$ 27,45", "€0.90": "R$ 5,50"}
	for in, want := range cases {
		if got := priceInReais(in); got != want {
			t.Errorf("%s -> %s, want %s", in, got, want)
		}
	}
}

func TestCleanItchText(t *testing.T) {
	in := `100% of donations go to support the itch.io platform. () Canyon Racer A playable vertical parallax tech demo, similar to the ""`
	if got := cleanItchText(in); got != "Canyon Racer A playable vertical parallax tech demo, similar to the" {
		t.Errorf("%q", got)
	}
}
