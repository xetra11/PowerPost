package platform

import "testing"

func TestComposeCaption(t *testing.T) {
	cases := []struct {
		text string
		tags []string
		want string
	}{
		{"Raid!", []string{"indiegame", "#GameDev"}, "Raid!\n\n#indiegame #GameDev"},
		{"Raid! #IndieGame", []string{"indiegame", "gamedev"}, "Raid! #IndieGame\n\n#gamedev"},
		{"", []string{"a", "A", " "}, "#a"},
		{"  Only text  ", nil, "Only text"},
	}
	for _, c := range cases {
		if got := ComposeCaption(c.text, c.tags); got != c.want {
			t.Errorf("ComposeCaption(%q, %v) = %q, want %q", c.text, c.tags, got, c.want)
		}
	}
}

func TestCaptionRules(t *testing.T) {
	utf16 := CaptionRules{MaxChars: 3, CountUTF16: true}
	runes := CaptionRules{MaxChars: 3}
	emoji := "a😀" // 2 runes, 3 UTF-16 units
	if utf16.Length(emoji) != 3 || runes.Length(emoji) != 2 {
		t.Fatalf("lengths: %d %d", utf16.Length(emoji), runes.Length(emoji))
	}
	if len(utf16.Check("x", emoji+"b")) != 1 || len(runes.Check("x", emoji+"b")) != 0 {
		t.Fatal("length check wrong")
	}
	tags := CaptionRules{MaxChars: 100, MaxHashtags: 2, MaxMentions: 1}
	issues := tags.Check("instagram", "#a #b #c @x @y email@example.com")
	if len(issues) != 2 {
		t.Fatalf("want hashtag + mention issues, got %v", issues)
	}
}
