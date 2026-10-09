package cli

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// mcpBenches puts a dev and a prod bench into an isolated state store.
func mcpBenches(t *testing.T) {
	t.Helper()
	cfg, benches := t.TempDir(), t.TempDir()
	t.Setenv("FFM_CONFIG_DIR", cfg)
	t.Setenv("FFM_BENCHES_DIR", benches)
	t.Setenv("FFM_BACKUPS_DIR", t.TempDir())
	for _, b := range []state.Bench{
		{Name: "dev1", Mode: "dev", SiteName: "dev1.localhost", WebPort: 8000, SocketIOPort: 9000,
			AdminPassword: "adm-s3cret", DBPassword: "db-s3cret", Bind: state.BindLoopback, FrappeBranch: "version-16"},
		{Name: "prod1", Mode: "prod", SiteName: "erp.example.com", Domain: "erp.example.com", WebPort: 8001, SocketIOPort: 9001,
			AdminPassword: "adm2-s3cret", DBPassword: "db2-s3cret", FrappeBranch: "version-16"},
	} {
		b.Dir = filepath.Join(benches, b.Name)
		if err := os.MkdirAll(b.Dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := state.Default().Add(b); err != nil {
			t.Fatal(err)
		}
	}
}

type mcpAsker struct {
	action  mcp.ElicitationResponseAction
	confirm bool
	asked   []string
}

func (a *mcpAsker) Elicit(_ context.Context, req mcp.ElicitationRequest) (*mcp.ElicitationResult, error) {
	a.asked = append(a.asked, req.Params.Message)
	return &mcp.ElicitationResult{ElicitationResponse: mcp.ElicitationResponse{
		Action: a.action, Content: map[string]any{"confirm": a.confirm},
	}}, nil
}

func mcpClient(t *testing.T, s *server.MCPServer, asker *mcpAsker) *mcpclient.Client {
	t.Helper()
	var opts []mcpclient.ClientOption
	if asker != nil {
		opts = append(opts, mcpclient.WithElicitationHandler(asker))
	}
	c, err := mcpclient.NewInProcessClientWithOptions(s, opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if err := c.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	init := mcp.InitializeRequest{}
	init.Params.ClientInfo = mcp.Implementation{Name: "ffm-test", Version: "1"}
	if _, err := c.Initialize(t.Context(), init); err != nil {
		t.Fatal(err)
	}
	return c
}

func mcpCall(t *testing.T, c *mcpclient.Client, name string, args map[string]any) (string, bool) {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Name, req.Params.Arguments = name, args
	res, err := c.CallTool(t.Context(), req)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var out strings.Builder
	for _, ct := range res.Content {
		if tc, ok := ct.(mcp.TextContent); ok {
			out.WriteString(tc.Text)
		}
	}
	return out.String(), res.IsError
}

func mcpToolNames(t *testing.T, c *mcpclient.Client) []string {
	t.Helper()
	res, err := c.ListTools(t.Context(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
	}
	sort.Strings(names)
	return names
}

func TestMCPToolSets(t *testing.T) {
	mcpBenches(t)
	ro := mcpClient(t, newMCPServer(manager.New(false), mcpOptions{}), nil)
	if got := strings.Join(mcpToolNames(t, ro), ","); got != "doctor,list,logs,snapshots,status" {
		t.Errorf("read-only tools = %s", got)
	}
	rw := mcpClient(t, newMCPServer(manager.New(false), mcpOptions{AllowWrite: true}), nil)
	if got := strings.Join(mcpToolNames(t, rw), ","); got != "clone,create,delete,doctor,list,logs,snapshot,snapshot_restore,snapshots,start,status,stop,test" {
		t.Errorf("write tools = %s", got)
	}
	// A read-only server must not run a write tool even if a client asks.
	req := mcp.CallToolRequest{}
	req.Params.Name, req.Params.Arguments = "stop", map[string]any{"bench": "dev1"}
	if res, err := ro.CallTool(t.Context(), req); err == nil && !res.IsError {
		t.Error("stop on a read-only server ran")
	}
}

func TestHelperProcess(t *testing.T) { fakeexec.HelperMain() }

func TestMCPListAndStatusHideSecrets(t *testing.T) {
	mcpBenches(t)
	fakeexec.Install(t)
	c := mcpClient(t, newMCPServer(manager.New(false), mcpOptions{}), nil)
	out, isErr := mcpCall(t, c, "list", nil)
	if isErr || !strings.Contains(out, `"schema":"ffm.list/v1"`) || !strings.Contains(out, `"name":"dev1"`) || !strings.Contains(out, `"name":"prod1"`) {
		t.Fatalf("list = %s (error %v)", out, isErr)
	}
	out, isErr = mcpCall(t, c, "status", map[string]any{"bench": "dev1"})
	if isErr || !strings.Contains(out, `"schema":"ffm.status/v1"`) {
		t.Fatalf("status = %s (error %v)", out, isErr)
	}
	if strings.Contains(out, "s3cret") {
		t.Errorf("status leaked a password: %s", out)
	}
	if out, isErr := mcpCall(t, c, "status", map[string]any{"bench": "nope"}); !isErr {
		t.Errorf("status of an unknown bench = %s", out)
	}
}

func TestMCPLogs(t *testing.T) {
	mcpBenches(t)
	fake := fakeexec.Install(t, fakeexec.Rule{Match: " logs ", Stdout: "frappe-1 | connecting with db-s3cret\nfrappe-1 | ready\n"})
	c := mcpClient(t, newMCPServer(manager.New(false), mcpOptions{}), nil)
	out, isErr := mcpCall(t, c, "logs", map[string]any{"bench": "dev1", "service": "frappe", "lines": 99999, "since": "10m"})
	if isErr {
		t.Fatalf("logs: %s", out)
	}
	if strings.Contains(out, "db-s3cret") || !strings.Contains(out, "ready") {
		t.Errorf("logs not scrubbed or empty: %s", out)
	}
	calls := strings.Join(fake.Calls(), "\n")
	if !strings.Contains(calls, "logs --no-color --tail 2000 --since 10m frappe") {
		t.Errorf("lines not capped at 2000 or args wrong:\n%s", calls)
	}
	for _, bad := range []map[string]any{
		{"bench": "dev1", "service": "frappe; rm -rf /"},
		{"bench": "dev1", "since": "1h --follow"},
	} {
		if out, isErr := mcpCall(t, c, "logs", bad); !isErr {
			t.Errorf("logs accepted %v: %s", bad, out)
		}
	}
}

func TestMCPProductionPolicy(t *testing.T) {
	mcpBenches(t)
	fake := fakeexec.Install(t)
	c := mcpClient(t, newMCPServer(manager.New(false), mcpOptions{AllowWrite: true}), nil)
	for _, tool := range []string{"stop", "start", "snapshot", "snapshot_restore"} {
		out, isErr := mcpCall(t, c, tool, map[string]any{"bench": "prod1"})
		if !isErr || !strings.HasPrefix(out, "policy:") {
			t.Errorf("%s on a prod bench = %s (error %v), want a policy refusal", tool, out, isErr)
		}
	}
	if out, isErr := mcpCall(t, c, "clone", map[string]any{"source": "prod1", "target": "copy"}); !isErr || !strings.HasPrefix(out, "policy:") {
		t.Errorf("clone of a prod bench = %s", out)
	}
	if calls := fake.Calls(); len(calls) != 0 {
		t.Errorf("work started on a prod bench:\n%s", strings.Join(calls, "\n"))
	}
	// Reading a prod bench is fine.
	if out, isErr := mcpCall(t, c, "snapshots", map[string]any{"bench": "prod1"}); isErr {
		t.Errorf("snapshots of a prod bench: %s", out)
	}
}

func TestMCPDeleteAsksTheUser(t *testing.T) {
	del := map[string]any{"bench": "dev1", "no_backup": true}

	t.Run("client cannot ask", func(t *testing.T) {
		mcpBenches(t)
		fake := fakeexec.Install(t)
		c := mcpClient(t, newMCPServer(manager.New(false), mcpOptions{AllowWrite: true}), nil)
		out, isErr := mcpCall(t, c, "delete", del)
		if !isErr || !strings.Contains(out, "cannot ask") || !strings.Contains(out, "ffm delete dev1") {
			t.Errorf("delete without elicitation = %s", out)
		}
		if _, err := state.Default().Get("dev1"); err != nil || len(fake.Calls()) != 0 {
			t.Errorf("bench touched: err=%v calls=%v", err, fake.Calls())
		}
	})
	t.Run("user declines", func(t *testing.T) {
		mcpBenches(t)
		fake := fakeexec.Install(t)
		a := &mcpAsker{action: mcp.ElicitationResponseActionDecline}
		c := mcpClient(t, newMCPServer(manager.New(false), mcpOptions{AllowWrite: true}), a)
		out, isErr := mcpCall(t, c, "delete", del)
		if !isErr || !strings.Contains(out, "cancelled by the user") {
			t.Errorf("declined delete = %s", out)
		}
		if len(a.asked) != 1 || !strings.Contains(a.asked[0], `"dev1"`) || !strings.Contains(a.asked[0], "No backup") {
			t.Errorf("asked %q", a.asked)
		}
		if _, err := state.Default().Get("dev1"); err != nil || len(fake.Calls()) != 0 {
			t.Errorf("bench touched: err=%v calls=%v", err, fake.Calls())
		}
	})
	t.Run("user accepts", func(t *testing.T) {
		mcpBenches(t)
		fakeexec.Install(t)
		a := &mcpAsker{action: mcp.ElicitationResponseActionAccept, confirm: true}
		c := mcpClient(t, newMCPServer(manager.New(false), mcpOptions{AllowWrite: true}), a)
		out, isErr := mcpCall(t, c, "delete", del)
		if isErr || !strings.Contains(out, `"deleted":true`) {
			t.Fatalf("accepted delete = %s", out)
		}
		if _, err := state.Default().Get("dev1"); err == nil {
			t.Error("dev1 still in the state after delete")
		}
	})
}

func TestConfirmStateIsSingleUseAndExpires(t *testing.T) {
	c := newConfirmer()
	req := mcp.CallToolRequest{}
	req.Params.Name, req.Params.Arguments = "delete", map[string]any{"bench": "dev1"}
	req.Params.RequestState = c.newState(req)
	if !c.spend(req) {
		t.Fatal("a fresh state was refused")
	}
	if c.spend(req) {
		t.Error("a state was accepted twice")
	}
	other := req
	other.Params.Arguments = map[string]any{"bench": "dev2"}
	other.Params.RequestState = c.newState(req) // issued for dev1
	if c.spend(other) {
		t.Error("a state issued for dev1 deleted dev2")
	}
	late := req
	late.Params.RequestState = c.newState(req)
	c.now = func() time.Time { return time.Now().Add(confirmTTL + time.Second) }
	if c.spend(late) {
		t.Error("an expired state was accepted")
	}
}
