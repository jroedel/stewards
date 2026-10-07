package mcpapp_test

import (
	"bytes"
	"encoding/json"
	"image"
	"image/jpeg"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jroedel/stewards/app/sdk/muxer"
	"github.com/jroedel/stewards/business/domain/inbox/inboxbus"
	"github.com/jroedel/stewards/business/domain/inbox/stores/inboxdb"
	"github.com/jroedel/stewards/business/domain/listing/listingbus"
	"github.com/jroedel/stewards/business/domain/listing/stores/listingdb"
	"github.com/jroedel/stewards/business/domain/nursery/nurserybus"
	"github.com/jroedel/stewards/business/domain/nursery/stores/nurserydb"
	"github.com/jroedel/stewards/business/domain/photo/photobus"
	"github.com/jroedel/stewards/business/domain/photo/stores/photodb"
	"github.com/jroedel/stewards/business/domain/photo/stores/photofs"
	"github.com/jroedel/stewards/business/domain/place/placebus"
	"github.com/jroedel/stewards/business/domain/place/stores/placedb"
	"github.com/jroedel/stewards/business/domain/species/speciesbus"
	"github.com/jroedel/stewards/business/domain/species/stores/speciesdb"
	"github.com/jroedel/stewards/business/domain/user/stores/userdb"
	"github.com/jroedel/stewards/business/domain/user/userbus"
	"github.com/jroedel/stewards/business/domain/workday/stores/workdaydb"
	"github.com/jroedel/stewards/business/domain/workday/workdaybus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/sqldb"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const base = "https://stewards.example.invalid"

type site struct {
	t     *testing.T
	srv   *httptest.Server
	key   string
	inbox *inboxbus.Business
	me    types.ID
}

// The whole site behind a real listener, so that the SDK's own client talks
// to it as claude.ai's would: over HTTP, through every middleware.
func serve(t *testing.T) *site {
	t.Helper()

	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	for _, init := range []func() error{
		func() error { return sqldb.Init(t.Context(), db) },
		func() error { return placedb.Init(t.Context(), db) },
		func() error { return speciesdb.Init(t.Context(), db) },
		func() error { return listingdb.Init(t.Context(), db) },
		func() error { return photodb.Init(t.Context(), db) },
		func() error { return userdb.Init(t.Context(), db) },
		func() error { return inboxdb.Init(t.Context(), db) },
		func() error { return nurserydb.Init(t.Context(), db) },
		func() error { return workdaydb.Init(t.Context(), db) },
	} {
		if err := init(); err != nil {
			t.Fatal(err)
		}
	}

	store := func(dir string) *photofs.Store {
		s, err := photofs.NewStore(filepath.Join(t.TempDir(), dir))
		if err != nil {
			t.Fatal(err)
		}

		return s
	}

	log := slog.New(slog.DiscardHandler)
	users := userbus.NewBusiness(log, userdb.NewStore(db), nil)
	photos := photobus.NewBusiness(photodb.NewStore(db), store("photo-files"), nil)
	listings := listingbus.NewBusiness(listingdb.NewStore(db), nil, nil)
	stock := nurserybus.NewBusiness(nurserydb.NewStore(db), nil)
	inbox := inboxbus.NewBusiness(inboxdb.NewStore(db), store("inbox"), inboxbus.Deps{Photos: photos, Listings: listings, Stock: stock}, nil)

	h, err := muxer.New(muxer.Config{
		Log: log, DB: db, Expected: sqldb.Infrastructure,
		Places: placebus.NewBusiness(placedb.NewStore(db), nil, nil), Species: speciesbus.NewBusiness(speciesdb.NewStore(db), nil, nil),
		Users: users, Workdays: workdaybus.NewBusiness(workdaydb.NewStore(db), nil, nil),
		Photos: photos, Listings: listings, Inbox: inbox, Nursery: stock,
		BaseURL: base,
	})
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	addr, _ := types.ParseEmail("steward@example.org")

	u, err := users.Create(t.Context(), addr, "")
	if err != nil {
		t.Fatal(err)
	}

	_, key, err := users.CreateAPIKey(t.Context(), u.ID, "Claude")
	if err != nil {
		t.Fatal(err)
	}

	return &site{t: t, srv: srv, key: key, inbox: inbox, me: u.ID}
}

// bearer adds the key to every request, as claude.ai does once signed in.
type bearer struct{ key string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.key)

	return http.DefaultTransport.RoundTrip(r)
}

func (s *site) connect() *mcp.ClientSession {
	s.t.Helper()

	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil)

	cs, err := c.Connect(s.t.Context(), &mcp.StreamableClientTransport{
		Endpoint:   s.srv.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearer{s.key}},
	}, nil)
	if err != nil {
		s.t.Fatal(err)
	}
	s.t.Cleanup(func() { cs.Close() })

	return cs
}

func (s *site) call(cs *mcp.ClientSession, tool string, args map[string]any) *mcp.CallToolResult {
	s.t.Helper()

	res, err := cs.CallTool(s.t.Context(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		s.t.Fatalf("%s: %v", tool, err)
	}

	return res
}

func text(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			b.WriteString(t.Text)
		}
	}

	return b.String()
}

// Without a key, a 401 that says where to sign in, and the document it
// points at says this site is where.
func TestWithoutAKeyClaudeIsToldWhereToSignIn(t *testing.T) {
	s := serve(t)

	resp, err := http.Post(s.srv.URL+"/mcp", "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") != `Bearer resource_metadata="`+base+`/.well-known/oauth-protected-resource/mcp"` {
		t.Fatalf("no key: %d %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}

	for _, path := range []string{"/.well-known/oauth-protected-resource/mcp", "/.well-known/oauth-protected-resource"} {
		resp, err := http.Get(s.srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}

		var doc struct {
			Resource string   `json:"resource"`
			Servers  []string `json:"authorization_servers"`
		}
		json.NewDecoder(resp.Body).Decode(&doc)
		resp.Body.Close()

		if doc.Resource != base+"/mcp" || !slices.Equal(doc.Servers, []string{base}) {
			t.Errorf("%s: %+v", path, doc)
		}
	}

	// A key that is not one: the same, saying so.
	req, _ := http.NewRequest(http.MethodPost, s.srv.URL+"/mcp", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer stw_nothing.ofthesort")

	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(resp.Header.Get("WWW-Authenticate"), `error="invalid_token"`) {
		t.Errorf("a bad key: %d %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
}

// The tools are the API's endpoints, by the names the index gives them,
// with the stewards' rules as the instructions.
func TestTheToolsAreTheAPIsEndpoints(t *testing.T) {
	s := serve(t)
	cs := s.connect()

	if got := cs.InitializeResult().Instructions; !strings.Contains(got, "Nothing you send is confirmed or checked") || !strings.Contains(got, "## The API's own rules") {
		t.Errorf("the instructions:\n%s", got)
	}

	tools, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}

	byName := map[string]*mcp.Tool{}
	for _, tool := range tools.Tools {
		byName[tool.Name] = tool
	}

	for _, want := range []string{"list_plants", "get_plant", "put_plant", "list_places", "put_place_plant", "remove_place_plant", "list_inbox", "sort_inbox_photo", "change_photo", "list_nursery_stock", "look_at_photo"} {
		if byName[want] == nil {
			t.Errorf("no %s", want)
		}
	}

	if len(byName) != 19 {
		t.Errorf("%d tools: the index's 18 and look_at_photo", len(byName))
	}

	// A path's parameter is a required argument, and a field's values are
	// its enum.
	raw, _ := json.Marshal(byName["sort_inbox_photo"].InputSchema)
	if !strings.Contains(string(raw), `"required":["id","outcome"]`) || !strings.Contains(string(raw), `"enum":["photo","planted","stock","unsure"]`) {
		t.Errorf("sort_inbox_photo's schema: %s", raw)
	}

	if a := byName["remove_place_plant"].Annotations; a.ReadOnlyHint || a.DestructiveHint == nil || !*a.DestructiveHint {
		t.Errorf("remove_place_plant's annotations: %+v", a)
	}
}

// A tool call is the API's request, as the steward: what it adds is there to
// read back, and a refusal is the API's own sentence, as an error Claude
// reads.
func TestAToolCallIsTheAPIsRequest(t *testing.T) {
	s := serve(t)
	cs := s.connect()

	res := s.call(cs, "put_plant", map[string]any{
		"slug":       "winecup",
		"common":     map[string]any{"en": "Winecup"},
		"scientific": "Callirhoe involucrata",
		"status":     "native",
	})
	if res.IsError || !strings.Contains(text(res), `"outcome": "created"`) {
		t.Fatalf("put_plant: %s", text(res))
	}

	if got := text(s.call(cs, "get_plant", map[string]any{"slug": "winecup"})); !strings.Contains(got, "Callirhoe involucrata") || !strings.Contains(got, `"confirmed": false`) {
		t.Errorf("get_plant: %s", got)
	}

	res = s.call(cs, "put_plant", map[string]any{"slug": "Not A Slug", "common": map[string]any{"en": "x"}, "status": "native"})
	if !res.IsError || !strings.Contains(text(res), "422") || !strings.Contains(text(res), "slug") {
		t.Errorf("a bad slug: %v %s", res.IsError, text(res))
	}

	if res := s.call(cs, "get_plant", map[string]any{}); !res.IsError {
		t.Errorf("no slug: %s", text(res))
	}
}

// Sorting the inbox from a chat: the list, the picture as a picture, and the
// sort.
func TestTheInboxIsSortedFromAChat(t *testing.T) {
	s := serve(t)
	cs := s.connect()

	it, err := s.inbox.Add(t.Context(), inboxbus.Fields{FromID: s.me, At: inboxbus.Elsewhere, Note: "on the trail"}, noisy(t))
	if err != nil {
		t.Fatal(err)
	}

	var list struct {
		Photos []struct {
			ID       string `json:"id"`
			LargeURL string `json:"large_url"`
		} `json:"photos"`
	}

	if err := json.Unmarshal([]byte(text(s.call(cs, "list_inbox", map[string]any{}))), &list); err != nil || len(list.Photos) != 1 || list.Photos[0].ID != it.ID.String() {
		t.Fatalf("list_inbox: %+v %v", list, err)
	}

	res := s.call(cs, "look_at_photo", map[string]any{"url": list.Photos[0].LargeURL})

	img, ok := res.Content[0].(*mcp.ImageContent)
	if res.IsError || !ok || img.MIMEType != "image/jpeg" || !bytes.HasPrefix(img.Data, []byte{0xff, 0xd8}) {
		t.Fatalf("look_at_photo: %v %s", res.IsError, text(res))
	}

	// Only from this site.
	for _, u := range []string{"https://example.org/x.jpg", "http://stewards.example.invalid/api/v1/inbox/" + it.ID.String() + "/large.jpg", base + "/api/v1"} {
		if res := s.call(cs, "look_at_photo", map[string]any{"url": u}); !res.IsError {
			t.Errorf("%s was looked at", u)
		}
	}

	if got := text(s.call(cs, "list_inbox", map[string]any{"status": "unsure"})); !strings.Contains(got, `"status": "unsure"`) {
		t.Errorf("list_inbox, unsure: %s", got)
	}

	res = s.call(cs, "sort_inbox_photo", map[string]any{"id": it.ID.String(), "outcome": "unsure", "note": "frostweed or wingstem?"})
	if res.IsError || !strings.Contains(text(res), `"outcome": "unsure"`) {
		t.Errorf("sort_inbox_photo: %s", text(res))
	}
}

func noisy(t *testing.T) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, 900, 600))
	rng := rand.New(rand.NewPCG(1, 2))

	for i := range img.Pix {
		img.Pix[i] = uint8(rng.UintN(256))
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}
