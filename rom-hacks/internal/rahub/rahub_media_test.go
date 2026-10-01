package rahub

import "testing"

// RetroAchievements substitutes filler pictures for sets with no artwork,
// among them a grey panel reading "No Screenshot Found". They are valid
// images that download and decode perfectly, so nothing downstream can
// tell them apart from real art — the gallery showed one and it looked
// exactly like this app failing to load a picture.
func TestPlaceholderImagesAreTreatedAsAbsent(t *testing.T) {
	placeholders := []string{
		"/Images/000001.png",
		"/Images/000002.png",
		"/Images/000005.png",
		"/Images/000020.png",
	}
	for _, p := range placeholders {
		if !IsPlaceholderImage(p) {
			t.Errorf("%s should be treated as a placeholder", p)
		}
		if got := MediaURL(p); got != "" {
			t.Errorf("MediaURL(%s) = %q, want empty", p, got)
		}
	}

	// Real uploads have six-digit ids far above the fillers and must
	// survive untouched.
	real := []string{"/Images/069405.png", "/Images/112233.png", "/Images/000123.png"}
	for _, p := range real {
		if IsPlaceholderImage(p) {
			t.Errorf("%s is a real upload, not a placeholder", p)
		}
		if got := MediaURL(p); got == "" {
			t.Errorf("MediaURL(%s) dropped a real image", p)
		}
	}
}
