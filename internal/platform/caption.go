package platform

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf16"
)

var (
	hashtagRe = regexp.MustCompile(`(?:^|\s)#[\p{L}\p{N}_]+`)
	mentionRe = regexp.MustCompile(`(?:^|\s)@[\p{L}\p{N}_.]+`)
)

// NormalizeHashtags trims, strips leading '#', drops empties and duplicates.
func NormalizeHashtags(tags []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, t := range tags {
		t = strings.TrimLeft(strings.TrimSpace(t), "#")
		if t == "" || seen[strings.ToLower(t)] {
			continue
		}
		seen[strings.ToLower(t)] = true
		out = append(out, t)
	}
	return out
}

// ComposeCaption appends hashtags that are not already in the text.
func ComposeCaption(text string, tags []string) string {
	text = strings.TrimSpace(text)
	lower := strings.ToLower(text)
	var add []string
	for _, t := range NormalizeHashtags(tags) {
		if !strings.Contains(lower, "#"+strings.ToLower(t)) {
			add = append(add, "#"+t)
		}
	}
	if len(add) == 0 {
		return text
	}
	if text == "" {
		return strings.Join(add, " ")
	}
	return text + "\n\n" + strings.Join(add, " ")
}

// CaptionLength counts characters the way the platform does.
func (r CaptionRules) Length(s string) int {
	if r.CountUTF16 {
		return len(utf16.Encode([]rune(s)))
	}
	return len([]rune(s))
}

// CheckCaption validates text limits.
func (r CaptionRules) Check(id ID, caption string) []ValidationIssue {
	var out []ValidationIssue
	add := func(code, msg string) {
		out = append(out, ValidationIssue{Platform: id, Severity: SeverityError, Scope: ScopeContent, Code: code, Message: msg})
	}
	if n := r.Length(caption); r.MaxChars > 0 && n > r.MaxChars {
		add("CAPTION_TOO_LONG", fmt.Sprintf("Caption is %d characters; maximum is %d.", n, r.MaxChars))
	}
	if n := len(hashtagRe.FindAllString(caption, -1)); r.MaxHashtags > 0 && n > r.MaxHashtags {
		add("TOO_MANY_HASHTAGS", fmt.Sprintf("Caption has %d hashtags; maximum is %d.", n, r.MaxHashtags))
	}
	if n := len(mentionRe.FindAllString(caption, -1)); r.MaxMentions > 0 && n > r.MaxMentions {
		add("TOO_MANY_MENTIONS", fmt.Sprintf("Caption has %d @mentions; maximum is %d.", n, r.MaxMentions))
	}
	return out
}
