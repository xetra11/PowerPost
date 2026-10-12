package mcpserver_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"powerposter/internal/app"
	"powerposter/internal/mcpserver"
	"powerposter/internal/testutil"
)

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (map[string]any, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error %v", name, err)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if res.IsError {
		return map[string]any{"error": text}, true
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("%s: %v in %s", name, err, text)
	}
	return out, false
}

func TestClaudeWorkflowOverHTTP(t *testing.T) {
	fake := &testutil.FakeAdapter{Name: "fake"}
	a := testutil.NewApp(t, fake)
	srv := httptest.NewServer(mcpserver.Handler(a))
	defer srv.Close()

	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
	}
	for _, want := range []string{"create_post", "attach_media", "validate_post", "publish_post", "get_platform_constraints"} {
		if !slices.Contains(names, want) {
			t.Fatalf("missing tool %s in %v", want, names)
		}
	}

	platforms, _ := call(t, cs, "list_platforms", nil)
	if platforms["inboxHostPath"] != testutil.HostInbox {
		t.Fatalf("inbox path %v", platforms["inboxHostPath"])
	}

	post, isErr := call(t, cs, "create_post", map[string]any{"title": "From Claude", "caption": "Thunder!", "hashtags": []string{"indiegame"}})
	if isErr {
		t.Fatal(post["error"])
	}
	id := post["id"].(float64)

	// Paths outside the inbox are refused with guidance.
	res, isErr := call(t, cs, "attach_media", map[string]any{"post_id": id, "path": `C:\Users\someone\video.mp4`})
	if !isErr || !strings.Contains(res["error"].(string), "inbox") {
		t.Fatalf("expected inbox error, got %v", res)
	}

	host := testutil.MakeVideo(t, a, "reel.mp4", 640, 360, 30, 1.5)
	if res, isErr = call(t, cs, "attach_media", map[string]any{"post_id": id, "path": host}); isErr {
		t.Fatal(res["error"])
	}

	var v map[string]any
	testutil.WaitFor(t, "ready", 60*time.Second, func() bool {
		v, _ = call(t, cs, "validate_post", map[string]any{"post_id": id})
		return v["ready"] == true
	})
	if !strings.HasSuffix(v["reviewUrl"].(string), "/review") {
		t.Fatalf("review url %v", v["reviewUrl"])
	}

	// Not approved yet → refused even though the post is ready.
	res, isErr = call(t, cs, "publish_post", map[string]any{"post_id": id})
	if !isErr || !strings.Contains(res["error"].(string), "turned off") {
		t.Fatalf("expected publishing to be off, got %v", res)
	}

	// The user allows Claude to publish, but hasn't approved: still refused.
	s, _ := a.GetSettings(ctx)
	s.MCPAllowPublish = true
	if _, err := a.SaveSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	res, isErr = call(t, cs, "publish_post", map[string]any{"post_id": id})
	if !isErr || !strings.Contains(res["error"].(string), "not approved") {
		t.Fatalf("expected approval error, got %v", res)
	}

	// User approves in the UI → Claude may publish.
	if _, err := a.Approve(ctx, int64(id)); err != nil {
		t.Fatal(err)
	}
	if res, isErr = call(t, cs, "publish_post", map[string]any{"post_id": id}); isErr {
		t.Fatal(res["error"])
	}
	testutil.WaitFor(t, "published", 10*time.Second, func() bool {
		st, _ := call(t, cs, "get_publish_status", map[string]any{"post_id": id})
		return st["status"] == "published"
	})

	brand, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "powerposter://brand"})
	if err != nil || !strings.Contains(brand.Contents[0].Text, "gameName") {
		t.Fatalf("brand resource: %v %v", brand, err)
	}

	// With a second project, Claude has to say which one a post belongs to.
	if _, err := a.CreateProject(ctx, app.ProjectInput{Name: "Sky Forge"}); err != nil {
		t.Fatal(err)
	}
	res, isErr = call(t, cs, "create_post", map[string]any{"caption": "Which one?"})
	if !isErr || !strings.Contains(res["error"].(string), "sky-forge") {
		t.Fatalf("expected project choice error, got %v", res)
	}
	post, isErr = call(t, cs, "create_post", map[string]any{"project": "sky-forge", "caption": "Forge!"})
	if isErr || post["project"] != "sky-forge" {
		t.Fatalf("create in project: %v", post)
	}
	projects, _ := call(t, cs, "list_projects", nil)
	list := projects["projects"].([]any)
	if len(list) != 2 || list[1].(map[string]any)["slug"] != "sky-forge" || len(list[1].(map[string]any)["accounts"].(map[string]any)) != 0 {
		t.Fatalf("list_projects: %v", projects)
	}
	posts, _ := call(t, cs, "list_posts", map[string]any{"project": "sky-forge"})
	if n := len(posts["posts"].([]any)); n != 1 {
		t.Fatalf("list_posts of project: %d posts", n)
	}
	platforms, _ = call(t, cs, "list_platforms", map[string]any{"project": "sky-forge"})
	if p := platforms["platforms"].([]any)[0].(map[string]any); p["connected"] != false {
		t.Fatalf("sky-forge should not be connected: %v", p)
	}
}
