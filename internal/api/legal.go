package api

import (
	"html/template"
	"net/http"
)

// legalUpdated is the date shown on the terms and privacy pages; bump it when the text changes.
const legalUpdated = "15 October 2026"

type legalData struct {
	Operator string // empty: "the operator of this installation"
	Contact  string
	Updated  string
}

// legalPage serves a static terms or privacy page over the tunnel.
func (s *Server) legalPage(t *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d := legalData{Operator: s.App.Cfg.LegalOperator, Contact: s.App.Cfg.LegalContact, Updated: legalUpdated}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		if err := t.ExecuteTemplate(w, "layout", d); err != nil {
			s.Log.Warn("legal page", "err", err)
		}
	}
}

const legalLayout = `{{define "layout"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{block "doctitle" .}}{{template "title"}} – PowerPoster{{end}}</title>
<style>
  :root { color-scheme: light dark; --fg: #1d1d1f; --muted: #6e6e73; --bg: #fff; --line: #e5e5ea; }
  @media (prefers-color-scheme: dark) { :root { --fg: #f5f5f7; --muted: #a1a1a6; --bg: #111113; --line: #2c2c2e; } }
  body { margin: 0; background: var(--bg); color: var(--fg); font: 16px/1.6 system-ui, -apple-system, "Segoe UI", sans-serif; }
  main { max-width: 720px; margin: 0 auto; padding: 40px 16px 64px; }
  h1 { font-size: 28px; margin: 0 0 4px; }
  h2 { font-size: 19px; margin: 32px 0 8px; }
  .meta { color: var(--muted); margin: 0 0 24px; }
  nav { border-top: 1px solid var(--line); margin-top: 40px; padding-top: 16px; color: var(--muted); }
  a { color: inherit; }
</style>
</head>
<body>
<main>
<h1>{{template "title"}}</h1>
<p class="meta">PowerPoster · Last updated {{.Updated}}</p>
{{template "body" .}}
<nav><a href="/">About</a> · <a href="/terms">Terms of Service</a> · <a href="/privacy">Privacy Policy</a></nav>
</main>
</body>
</html>{{end}}
{{define "operator"}}{{if .Operator}}{{.Operator}}{{else}}the operator of this PowerPoster installation{{end}}{{end}}
{{define "contact"}}{{if .Contact}}<a href="mailto:{{.Contact}}">{{.Contact}}</a>{{else}}the operator of this installation{{end}}{{end}}`

// homePage describes PowerPoster for the platforms' app forms. It deliberately
// has no sign-in and no link to the web app, which only runs on localhost.
var homePage = template.Must(template.Must(template.New("home").Parse(legalLayout)).Parse(`
{{define "title"}}PowerPoster{{end}}
{{define "doctitle"}}PowerPoster{{end}}
{{define "body"}}
<p><strong>Plan, review and publish social media posts for game projects from one place.</strong></p>
<p>PowerPoster is an in-house publishing tool run by {{template "operator" .}}. It prepares videos and image posts for our games and publishes them to TikTok, Instagram, YouTube (Shorts) and Discord.</p>

<h2>How it works</h2>
<ol>
  <li><strong>Create a post</strong> for a game project, with its caption, hashtags and target platforms.</li>
  <li><strong>Add a video or images.</strong> PowerPoster converts them to each platform's format, aspect ratio and size limits.</li>
  <li><strong>Review</strong> exactly what will be uploaded: a phone preview per platform, caption, cover and the platform's required settings (for TikTok: visibility, comments, Duet, Stitch and content disclosure).</li>
  <li><strong>Publish</strong> after approval. Videos and photos go to the connected TikTok account, either to the TikTok inbox to finish in the app, or posted directly.</li>
</ol>

<h2>Accounts</h2>
<p>TikTok, Instagram and YouTube accounts are connected through the platforms' official sign-in (TikTok Login Kit, Google sign-in), and access can be revoked at any time. PowerPoster only publishes content that a person has reviewed and approved.</p>

<h2>Access</h2>
<p>PowerPoster is not a public service: it runs on our own computer and is used only by our team. There is no public sign-up.</p>

<p>Contact: {{template "contact" .}}</p>
{{end}}`))

var termsPage = template.Must(template.Must(template.New("terms").Parse(legalLayout)).Parse(`
{{define "title"}}Terms of Service{{end}}
{{define "body"}}
<p>PowerPoster is a publishing tool run by {{template "operator" .}} ("we", "us"). It is used to prepare videos and images for social media, review them, and publish them to TikTok, Instagram, YouTube and Discord. By connecting an account or using PowerPoster you agree to these terms.</p>

<h2>1. The service</h2>
<p>PowerPoster converts your media to each platform's format, shows a preview of exactly what will be uploaded, and publishes it to the accounts you connected. Nothing is published until a post has been approved on the review page.</p>

<h2>2. Your accounts</h2>
<p>You connect TikTok, Instagram and YouTube accounts through the platforms' own sign-in pages and Discord channels through a webhook. You may only connect accounts and channels that you own or are authorised to manage. You can disconnect an account in PowerPoster's settings at any time, and revoke access in the platform's own app settings.</p>

<h2>3. Your content</h2>
<p>You keep all rights to the content you publish. You are responsible for it: you must hold the rights to all videos, images, music and text, and the content must comply with applicable law and with the terms and community guidelines of each platform, including the <a href="https://www.tiktok.com/legal/terms-of-service">TikTok Terms of Service</a> and <a href="https://www.tiktok.com/community-guidelines">Community Guidelines</a>, and the <a href="https://www.youtube.com/t/terms">YouTube Terms of Service</a> and <a href="https://www.youtube.com/howyoutubeworks/policies/community-guidelines/">Community Guidelines</a>. You grant us only the permission needed to store, convert and upload your content to the platforms you choose.</p>

<h2>4. Acceptable use</h2>
<p>Do not use PowerPoster for spam, misleading or illegal content, to infringe the rights of others, or to bypass platform limits or rules. We may suspend access if these terms are violated.</p>

<h2>5. Third-party platforms</h2>
<p>PowerPoster uses the YouTube API Services to publish to YouTube. By connecting a YouTube account you agree to be bound by the <a href="https://www.youtube.com/t/terms">YouTube Terms of Service</a>.</p>
<p>Publishing depends on the APIs of TikTok, Instagram, YouTube and Discord. We are not responsible for how these platforms process, rank, restrict or remove content, nor for changes or outages of their services.</p>

<h2>6. Availability and liability</h2>
<p>PowerPoster is provided "as is" without any guarantee of availability. To the extent permitted by law, we are not liable for indirect damages, lost profits, or content that could not be published or was published incorrectly. Liability for intent and gross negligence, and for injury to life, body or health, remains unaffected.</p>

<h2>7. Changes</h2>
<p>We may update these terms. The date at the top of this page shows the latest version. Continuing to use PowerPoster after a change means you accept the updated terms.</p>

<h2>8. Contact</h2>
<p>Questions about these terms: {{template "contact" .}}.</p>
{{end}}`))

var privacyPage = template.Must(template.Must(template.New("privacy").Parse(legalLayout)).Parse(`
{{define "title"}}Privacy Policy{{end}}
{{define "body"}}
<p>This policy explains what data PowerPoster processes and why. PowerPoster is run by {{template "operator" .}}, who is responsible for the processing described here.</p>

<h2>1. Where data is stored</h2>
<p>PowerPoster is self-hosted: all data is stored in a local database and file folder on the operator's own computer. There is no PowerPoster cloud service, no user tracking, no analytics and no advertising. We do not sell or rent any data.</p>

<h2>2. Data we process</h2>
<ul>
  <li><strong>Platform account data</strong> received when you connect an account, such as the TikTok open ID and display name, the Instagram user ID and username, or the YouTube channel ID and channel name.</li>
  <li><strong>Access tokens</strong> issued by TikTok, Instagram and Google (YouTube), and Discord webhook URLs. They are stored encrypted and used only to publish the posts you approve.</li>
  <li><strong>Content</strong>: the videos, images, captions, hashtags and post settings you create, and the converted files.</li>
  <li><strong>Publishing results</strong>: post IDs, links, status, error messages and, where a platform provides them, basic statistics of published posts.</li>
</ul>

<h2>3. TikTok data</h2>
<p>When you connect TikTok, PowerPoster requests the scopes <code>user.info.basic</code> (to show which account is connected), <code>video.upload</code> and <code>video.publish</code> (to upload videos and photos to your account or inbox). The data is used only to publish content you approved and to show the connected account. It is not shared with anyone else and not used for any other purpose.</p>

<h2>4. YouTube data</h2>
<p>PowerPoster uses the YouTube API Services. When you connect YouTube through Google sign-in, PowerPoster requests the scopes <code>youtube.upload</code> (to upload the videos you approved to your channel, with the title, description, visibility and audience settings you chose) and <code>youtube.readonly</code> (to show which channel is connected, and to read the processing status, link and view, like and comment counts of the videos PowerPoster uploaded). PowerPoster does not access, change or delete any other videos, playlists, comments or channel data. The data is stored only on the operator's computer, used only for these purposes and not shared with anyone else.</p>
<p>Google's handling of this data is described in the <a href="https://policies.google.com/privacy">Google Privacy Policy</a>. PowerPoster's use and transfer of information received from Google APIs adheres to the <a href="https://developers.google.com/terms/api-services-user-data-policy">Google API Services User Data Policy</a>, including the Limited Use requirements. You can revoke PowerPoster's access at any time on the <a href="https://security.google.com/settings/security/permissions">Google security settings page</a>.</p>

<h2>5. Sharing with third parties</h2>
<p>Content and post settings are sent only to the platform you publish to (TikTok, Instagram, YouTube or Discord). Platforms that download media themselves receive a signed link that expires after a short time. Our tunnel provider (ngrok) forwards these requests and sign-in redirects to our computer. The platforms' own privacy policies apply to data they receive, for example the <a href="https://www.tiktok.com/legal/privacy-policy">TikTok Privacy Policy</a> and the <a href="https://policies.google.com/privacy">Google Privacy Policy</a>.</p>

<h2>6. Retention and deletion</h2>
<p>Data is kept until it is deleted. Disconnecting an account in PowerPoster's settings deletes its tokens immediately; deleting a post removes its content and converted files. You can also revoke PowerPoster's access at any time in your TikTok or Instagram app settings, and for YouTube on the <a href="https://security.google.com/settings/security/permissions">Google security settings page</a>. Content already published stays on the platform until you delete it there.</p>

<h2>7. Cookies</h2>
<p>These public pages and the sign-in redirects set no cookies.</p>

<h2>8. Your rights</h2>
<p>You can request access to, correction or deletion of your data, restrict or object to its processing, and ask for a copy of it. You also have the right to lodge a complaint with a data protection authority. To exercise your rights, contact {{template "contact" .}}.</p>

<h2>9. Changes</h2>
<p>We may update this policy. The date at the top of this page shows the latest version.</p>
{{end}}`))
