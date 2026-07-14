package xiaozhi

import "testing"

func TestMatchLocalSelfControl(t *testing.T) {
	cases := []struct {
		stt    string
		intent string
	}{
		{"Kể chuyện vui đi", ""},
		{"Pháo hoa đi nào", "intent_seasonal_happynewyear"},
		{"Mày có thể bắn tháo hoa cho ta xem được không?", "intent_seasonal_happynewyear"},
		{"bắn pháo hoa được không", "intent_seasonal_happynewyear"},
		{"Chúc mừng năm mới", "intent_seasonal_happynewyear"},
		{"happy new year Vector", "intent_seasonal_happynewyear"},
		{"Nhảy múa đi", "intent_imperative_dance"},
		{"dance for me", "intent_imperative_dance"},
		{"Về nhà đi", "intent_system_charger"},
		{"Chụp ảnh cho tao", "intent_photo_take_extend"},
		{"mấy giờ rồi", "intent_clock_time"},
	}
	for _, c := range cases {
		intent, _, ok := MatchLocalSelfControl(c.stt)
		if c.intent == "" {
			if ok {
				t.Fatalf("%q: expected no match, got %s", c.stt, intent)
			}
			continue
		}
		if !ok || intent != c.intent {
			t.Fatalf("%q: got (%v,%q) want %s", c.stt, ok, intent, c.intent)
		}
	}
}
