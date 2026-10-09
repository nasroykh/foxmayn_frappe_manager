package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/version"
)

// mcpOptions decide which tools ffm mcp offers.
type mcpOptions struct {
	// AllowWrite adds the tools that create, change, start, stop and delete
	// benches. Without it the server is read-only.
	AllowWrite bool
	// AllowProd lets the write tools act on production benches.
	AllowProd bool
}

func newMCPCmd() *cobra.Command {
	var opts mcpOptions
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve ffm's bench tools to an MCP client (stdio)",
		Long: `Run an MCP server on stdin/stdout so an agent on this host can manage benches.

Read-only by default: list, status, doctor, logs and snapshots.
--allow-write adds create (dev benches), clone, snapshot, snapshot_restore, test,
start, stop and delete. delete always asks the user through the MCP client
(elicitation) and is refused by clients that cannot ask.
Write tools leave production benches alone unless --allow-prod is given.

Register it with a client, e.g. Claude Code:
  claude mcp add ffm -- ffm mcp --allow-write`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMCP(opts)
		},
	}
	cmd.Flags().BoolVar(&opts.AllowWrite, "allow-write", false, "Also offer the tools that create, change, start, stop and delete benches")
	cmd.Flags().BoolVar(&opts.AllowProd, "allow-prod", false, "Let the write tools act on production benches (needs --allow-write)")
	return cmd
}

func runMCP(opts mcpOptions) error {
	if opts.AllowProd && !opts.AllowWrite {
		return fmt.Errorf("--allow-prod needs --allow-write")
	}
	// stdout carries the protocol. ffm's own output and every subprocess
	// that inherits os.Stdout go to stderr instead, where clients log it.
	out := os.Stdout
	os.Stdout = os.Stderr
	_ = os.Setenv("FFM_NON_INTERACTIVE", "1")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	s := newMCPServer(manager.New(false), opts)
	return server.NewStdioServer(s).Listen(ctx, os.Stdin, out)
}

// toolKind sorts tools by what they may do.
type toolKind int

const (
	toolRead toolKind = iota
	toolWrite
	toolDelete
)

type mcpServer struct {
	svc  *manager.Service
	opts mcpOptions
	// mu runs one write tool at a time: two image builds or restores at once
	// only compete for the host. Read tools never wait for it.
	mu sync.Mutex
}

type mcpTool struct {
	kind toolKind
	tool mcp.Tool
	run  func(ctx context.Context, req mcp.CallToolRequest, pw *mcpProgress) (any, error)
}

func newMCPServer(svc *manager.Service, opts mcpOptions) *server.MCPServer {
	m := &mcpServer{svc: svc, opts: opts}
	tools := m.tools()
	s := server.NewMCPServer("ffm", version.Version,
		server.WithToolCapabilities(false),
		server.WithRecovery(),
		server.WithInstructions(m.instructions(tools)),
	)
	for _, t := range tools {
		s.AddTool(t.tool, m.handler(t))
	}
	return s
}

// tools returns the tools this server offers under its options.
func (m *mcpServer) tools() []mcpTool {
	all := []mcpTool{
		{toolRead, mcp.NewTool("list",
			mcp.WithDescription("List every bench with its mode, status, URL, ports and Frappe branch (schema ffm.list/v1)."),
			mcp.WithTitleAnnotation("List benches"), mcp.WithReadOnlyHintAnnotation(true), mcp.WithOpenWorldHintAnnotation(false),
		), m.list},
		{toolRead, mcp.NewTool("status",
			mcp.WithDescription("One bench in detail: apps, toolchain, containers and their health (schema ffm.status/v1). Passwords are left out."),
			mcp.WithTitleAnnotation("Bench status"), mcp.WithReadOnlyHintAnnotation(true), mcp.WithOpenWorldHintAnnotation(false),
			benchParam(),
		), m.status},
		{toolRead, mcp.NewTool("doctor",
			mcp.WithDescription("Health checks for one bench or all: containers, site ping, database, scheduler, workers per queue, TLS, backup age, disk, templates (schema ffm.doctor/v1). ok is false when a check fails."),
			mcp.WithTitleAnnotation("Check bench health"), mcp.WithReadOnlyHintAnnotation(true), mcp.WithOpenWorldHintAnnotation(false),
			mcp.WithString("bench", mcp.Description("Bench name; every bench when omitted")),
		), m.doctor},
		{toolRead, mcp.NewTool("logs",
			mcp.WithDescription(fmt.Sprintf("The last lines of a bench's container logs, secrets scrubbed. Default %d lines, at most %d.", defaultLogLines, maxLogLines)),
			mcp.WithTitleAnnotation("Bench logs"), mcp.WithReadOnlyHintAnnotation(true), mcp.WithOpenWorldHintAnnotation(false),
			benchParam(),
			mcp.WithString("service", mcp.Description("One compose service (frappe, socketio, mariadb, redis-queue, worker-short, ...); all when omitted")),
			mcp.WithNumber("lines", mcp.Description("Lines to return per service")),
			mcp.WithString("since", mcp.Description(`Only logs newer than this: a duration ("10m", "2h") or an RFC 3339 time`)),
		), m.logs},
		{toolRead, mcp.NewTool("snapshots",
			mcp.WithDescription("A bench's snapshots, newest first, with the app commits each was taken at (schema ffm.snapshots/v1)."),
			mcp.WithTitleAnnotation("List snapshots"), mcp.WithReadOnlyHintAnnotation(true), mcp.WithOpenWorldHintAnnotation(false),
			benchParam(),
		), m.snapshots},

		{toolWrite, mcp.NewTool("create",
			mcp.WithDescription("Create a development bench (takes minutes; progress is reported). Ports are published on 127.0.0.1 only. Returns its status."),
			mcp.WithTitleAnnotation("Create a dev bench"), mcp.WithDestructiveHintAnnotation(false), mcp.WithIdempotentHintAnnotation(false), mcp.WithOpenWorldHintAnnotation(true),
			mcp.WithString("name", mcp.Required(), mcp.Description("Bench name: lowercase letters, digits and hyphens")),
			mcp.WithString("frappe_branch", mcp.Description("Frappe branch, e.g. version-15; default version-16")),
			mcp.WithArray("apps", mcp.WithStringItems(), mcp.Description(`Apps to install: "erpnext", "hrms@version-16" or a git URL`)),
			mcp.WithString("python", mcp.Description("Python for the virtualenv (3.12 or 3.14); default per branch")),
			mcp.WithBoolean("agent", mcp.Description("Agent-ready bench: a dedicated agent user and an MCP config inside the bench")),
		), m.create},
		{toolWrite, mcp.NewTool("clone",
			mcp.WithDescription("Copy a development bench into a new one (backup and restore: site, apps at their commits, files). Takes minutes."),
			mcp.WithTitleAnnotation("Clone a bench"), mcp.WithDestructiveHintAnnotation(false), mcp.WithIdempotentHintAnnotation(false), mcp.WithOpenWorldHintAnnotation(false),
			mcp.WithString("source", mcp.Required(), mcp.Description("Bench to copy")),
			mcp.WithString("target", mcp.Required(), mcp.Description("Name of the new bench")),
			mcp.WithBoolean("no_files", mcp.Description("Leave the site's attachments behind")),
		), m.clone},
		{toolWrite, mcp.NewTool("snapshot",
			mcp.WithDescription("Snapshot a bench's site database (and files) in place, to roll back to later with snapshot_restore."),
			mcp.WithTitleAnnotation("Snapshot a bench"), mcp.WithDestructiveHintAnnotation(false), mcp.WithIdempotentHintAnnotation(false), mcp.WithOpenWorldHintAnnotation(false),
			benchParam(),
			mcp.WithString("name", mcp.Description("Snapshot name; default the UTC time")),
			mcp.WithBoolean("files", mcp.Description("Also capture the site's public and private files")),
		), m.snapshot},
		{toolWrite, mcp.NewTool("snapshot_restore",
			mcp.WithDescription("Put a bench's site back to a snapshot. Replaces the current database: changes since the snapshot are lost."),
			mcp.WithTitleAnnotation("Restore a snapshot"), mcp.WithDestructiveHintAnnotation(true), mcp.WithIdempotentHintAnnotation(true), mcp.WithOpenWorldHintAnnotation(false),
			benchParam(),
			mcp.WithString("name", mcp.Description("Snapshot to restore; default the newest")),
			mcp.WithBoolean("migrate", mcp.Description("Run bench migrate afterwards, for code that moved on since the snapshot")),
		), m.snapshotRestore},
		{toolWrite, mcp.NewTool("test",
			mcp.WithDescription(fmt.Sprintf("Run an app's tests (bench run-tests) on a development bench. Returns ok and the last %d KiB of output.", maxTestOutput>>10)),
			mcp.WithTitleAnnotation("Run tests"), mcp.WithDestructiveHintAnnotation(false), mcp.WithIdempotentHintAnnotation(true), mcp.WithOpenWorldHintAnnotation(false),
			benchParam(),
			mcp.WithString("app", mcp.Required(), mcp.Description("App whose tests run")),
			mcp.WithString("module", mcp.Description("Only this module, e.g. erpnext.selling.doctype.quotation.test_quotation")),
			mcp.WithString("doctype", mcp.Description("Only this DocType's tests")),
			mcp.WithString("test", mcp.Description("Only this test function (needs module or doctype)")),
			mcp.WithBoolean("failfast", mcp.Description("Stop at the first failure")),
		), m.test},
		{toolWrite, mcp.NewTool("start",
			mcp.WithDescription("Start a stopped bench and wait for its site."),
			mcp.WithTitleAnnotation("Start a bench"), mcp.WithDestructiveHintAnnotation(false), mcp.WithIdempotentHintAnnotation(true), mcp.WithOpenWorldHintAnnotation(false),
			benchParam(),
		), m.start},
		{toolWrite, mcp.NewTool("stop",
			mcp.WithDescription("Stop a bench's containers. Data is kept."),
			mcp.WithTitleAnnotation("Stop a bench"), mcp.WithDestructiveHintAnnotation(false), mcp.WithIdempotentHintAnnotation(true), mcp.WithOpenWorldHintAnnotation(false),
			benchParam(),
		), m.stop},
		{toolDelete, mcp.NewTool("delete",
			mcp.WithDescription("Delete a bench: containers, volumes and its directory. A backup is taken first unless no_backup. The user is asked to confirm."),
			mcp.WithTitleAnnotation("Delete a bench"), mcp.WithDestructiveHintAnnotation(true), mcp.WithIdempotentHintAnnotation(false), mcp.WithOpenWorldHintAnnotation(false),
			benchParam(),
			mcp.WithBoolean("no_backup", mcp.Description("Skip the backup taken before deleting")),
		), m.delete},
	}
	var out []mcpTool
	for _, t := range all {
		if t.kind == toolRead || m.opts.AllowWrite {
			out = append(out, t)
		}
	}
	return out
}

func benchParam() mcp.ToolOption {
	return mcp.WithString("bench", mcp.Required(), mcp.Description("Bench name (see list)"))
}

func (m *mcpServer) instructions(tools []mcpTool) string {
	var sb strings.Builder
	sb.WriteString("ffm manages Frappe benches on this host: each bench is a Docker Compose project with one Frappe site.\n")
	sb.WriteString("Start with list; status and doctor tell what a bench runs and whether it is healthy; logs shows what its containers printed.\n")
	if !m.opts.AllowWrite {
		sb.WriteString("This server is read-only: it cannot create, change, start, stop or delete benches. The user can restart it with --allow-write.\n")
		return sb.String()
	}
	sb.WriteString("create and clone take minutes and report progress. Prefer snapshot before a risky change to a bench, and snapshot_restore to undo it.\n")
	sb.WriteString("delete asks the user to confirm; a refusal or a decline changes nothing.\n")
	if !m.opts.AllowProd {
		sb.WriteString("Write tools refuse production benches (mode prod); manage those from a terminal.\n")
	}
	sb.WriteString(`Errors that start with "policy:" are this server's limits, not failures: do not retry them.` + "\n")
	return sb.String()
}

// handler runs a tool and turns its result or error into a tool result, so
// the model sees what went wrong.
func (m *mcpServer) handler(t mcpTool) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if t.kind == toolDelete {
			if r := mcpConfirm.confirm(ctx, req); r != nil {
				return r, nil
			}
		}
		if t.kind != toolRead {
			m.mu.Lock()
			defer m.mu.Unlock()
		}
		pw := newMCPProgress(ctx, req)
		v, err := t.run(ctx, req, pw)
		if err != nil {
			msg := err.Error()
			if tail := pw.tail(); tail != "" && t.kind != toolRead {
				msg += "\n\nLast output:\n" + tail
			}
			return mcp.NewToolResultError(msg), nil
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultStructured(v, string(raw)), nil
	}
}

// bench looks a bench up for a tool. Write tools leave production benches
// alone unless the server was started with --allow-prod.
func (m *mcpServer) bench(req mcp.CallToolRequest, key string, write bool) (state.Bench, error) {
	name, err := req.RequireString(key)
	if err != nil {
		return state.Bench{}, err
	}
	b, err := m.svc.GetBench(name)
	if err != nil {
		return b, err
	}
	if write && b.IsProd() && !m.opts.AllowProd {
		return b, fmt.Errorf("policy: %q is a production bench; this server changes production benches only with --allow-prod. Use the ffm CLI in a terminal", b.Name)
	}
	return b, nil
}

// ─── read tools ─────────────────────────────────────────────────────────────

func (m *mcpServer) list(_ context.Context, _ mcp.CallToolRequest, _ *mcpProgress) (any, error) {
	views, err := m.svc.ListBenchViews()
	if err != nil {
		return nil, err
	}
	return listDoc(views), nil
}

func (m *mcpServer) status(_ context.Context, req mcp.CallToolRequest, _ *mcpProgress) (any, error) {
	b, err := m.bench(req, "bench", false)
	if err != nil {
		return nil, err
	}
	return statusDoc(m.svc, b.Name, false)
}

func (m *mcpServer) doctor(_ context.Context, req mcp.CallToolRequest, _ *mcpProgress) (any, error) {
	var names []string
	if name := req.GetString("bench", ""); name != "" {
		names = []string{name}
	} else {
		benches, err := m.svc.LoadBenches()
		if err != nil {
			return nil, err
		}
		for _, b := range benches {
			names = append(names, b.Name)
		}
	}
	out := jsonDoctor{Schema: "ffm.doctor/v1", OK: true, Benches: []manager.BenchHealth{}}
	for _, n := range names {
		h, err := m.svc.Doctor(n)
		if err != nil {
			return nil, err
		}
		if h.Worst() == manager.CheckFail {
			out.OK = false
		}
		out.Benches = append(out.Benches, h)
	}
	return out, nil
}

const (
	defaultLogLines = 200
	maxLogLines     = 2000
)

var (
	serviceRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	sinceRe   = regexp.MustCompile(`^[0-9A-Za-z:.+-]{1,40}$`)
)

type mcpLogs struct {
	Bench   string `json:"bench"`
	Service string `json:"service,omitempty"`
	Lines   int    `json:"lines"`
	Logs    string `json:"logs"`
}

func (m *mcpServer) logs(_ context.Context, req mcp.CallToolRequest, _ *mcpProgress) (any, error) {
	b, err := m.bench(req, "bench", false)
	if err != nil {
		return nil, err
	}
	service := req.GetString("service", "")
	if service != "" && !serviceRe.MatchString(service) {
		return nil, fmt.Errorf("invalid service %q", service)
	}
	since := req.GetString("since", "")
	if since != "" && !sinceRe.MatchString(since) {
		return nil, fmt.Errorf("invalid since %q: use a duration such as 10m or an RFC 3339 time", since)
	}
	lines := req.GetInt("lines", defaultLogLines)
	if lines <= 0 || lines > maxLogLines {
		lines = min(max(lines, 1), maxLogLines)
	}
	runner := bench.NewRunner(b.Name, b.Dir, false)
	runner.Redact = []string{b.DBPassword, b.AdminPassword}
	text, err := runner.LogsTail(service, lines, since)
	if err != nil {
		return nil, fmt.Errorf("docker compose logs: %w: %s", err, lastLine(text))
	}
	return mcpLogs{Bench: b.Name, Service: service, Lines: lines, Logs: text}, nil
}

func (m *mcpServer) snapshots(_ context.Context, req mcp.CallToolRequest, _ *mcpProgress) (any, error) {
	b, err := m.bench(req, "bench", false)
	if err != nil {
		return nil, err
	}
	snaps, err := m.svc.ListSnapshots(b.Name)
	if err != nil {
		return nil, err
	}
	return snapshotsDoc(b.Name, snaps), nil
}

// ─── write tools ────────────────────────────────────────────────────────────

func (m *mcpServer) create(_ context.Context, req mcp.CallToolRequest, pw *mcpProgress) (any, error) {
	name, err := req.RequireString("name")
	if err != nil {
		return nil, err
	}
	in := manager.CreateInput{
		Name: name, Mode: "dev",
		FrappeBranch:  req.GetString("frappe_branch", bench.DefaultFrappeBranch),
		Apps:          req.GetStringSlice("apps", nil),
		Python:        req.GetString("python", ""),
		AdminPassword: "admin", DBPassword: "ffm123456", DBType: "mariadb",
		Bind:  manager.BindFor(false),
		Agent: req.GetBool("agent", false),
	}
	if err := m.svc.Create(in, pw); err != nil {
		return nil, err
	}
	return statusDoc(m.svc, name, false)
}

func (m *mcpServer) clone(_ context.Context, req mcp.CallToolRequest, pw *mcpProgress) (any, error) {
	src, err := m.bench(req, "source", false)
	if err != nil {
		return nil, err
	}
	if src.IsProd() {
		// A production copy needs a domain and certificates; it is not a
		// throwaway an agent should make.
		return nil, fmt.Errorf("policy: %q is a production bench; clone it from a terminal (ffm clone %s <new> --domain ...)", src.Name, src.Name)
	}
	target, err := req.RequireString("target")
	if err != nil {
		return nil, err
	}
	if err := m.svc.Clone(manager.CloneInput{Source: src.Name, Target: target, NoFiles: req.GetBool("no_files", false)}, pw); err != nil {
		return nil, err
	}
	return statusDoc(m.svc, target, false)
}

type mcpSnapshotResult struct {
	Bench    string `json:"bench"`
	Snapshot string `json:"snapshot"`
	Restored bool   `json:"restored,omitempty"`
}

func (m *mcpServer) snapshot(_ context.Context, req mcp.CallToolRequest, pw *mcpProgress) (any, error) {
	b, err := m.bench(req, "bench", true)
	if err != nil {
		return nil, err
	}
	name, err := m.svc.CreateSnapshot(manager.SnapshotInput{Bench: b.Name, Name: req.GetString("name", ""), Files: req.GetBool("files", false)}, pw)
	if err != nil {
		return nil, err
	}
	return mcpSnapshotResult{Bench: b.Name, Snapshot: name}, nil
}

func (m *mcpServer) snapshotRestore(_ context.Context, req mcp.CallToolRequest, pw *mcpProgress) (any, error) {
	b, err := m.bench(req, "bench", true)
	if err != nil {
		return nil, err
	}
	name, err := m.svc.RestoreSnapshot(manager.RestoreSnapshotInput{Bench: b.Name, Name: req.GetString("name", ""), Migrate: req.GetBool("migrate", false)}, pw)
	if err != nil {
		return nil, err
	}
	return mcpSnapshotResult{Bench: b.Name, Snapshot: name, Restored: true}, nil
}

const maxTestOutput = 48 << 10

type mcpTestResult struct {
	Bench  string `json:"bench"`
	App    string `json:"app"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	Output string `json:"output"`
}

func (m *mcpServer) test(_ context.Context, req mcp.CallToolRequest, _ *mcpProgress) (any, error) {
	b, err := m.bench(req, "bench", true)
	if err != nil {
		return nil, err
	}
	app, err := req.RequireString("app")
	if err != nil {
		return nil, err
	}
	out := &tailBuffer{max: maxTestOutput}
	terr := m.svc.Test(manager.TestInput{
		Name: b.Name, App: app,
		Module: req.GetString("module", ""), Doctype: req.GetString("doctype", ""), Test: req.GetString("test", ""),
		Failfast: req.GetBool("failfast", false), Out: out,
	})
	// Failing tests are a result, not a tool error: the model reads them.
	res := mcpTestResult{Bench: b.Name, App: app, OK: terr == nil, Output: out.String()}
	if terr != nil {
		res.Error = terr.Error()
	}
	return res, nil
}

func (m *mcpServer) start(_ context.Context, req mcp.CallToolRequest, pw *mcpProgress) (any, error) {
	b, err := m.bench(req, "bench", true)
	if err != nil {
		return nil, err
	}
	if err := m.svc.Start(b.Name, pw); err != nil {
		return nil, err
	}
	return statusDoc(m.svc, b.Name, false)
}

func (m *mcpServer) stop(_ context.Context, req mcp.CallToolRequest, pw *mcpProgress) (any, error) {
	b, err := m.bench(req, "bench", true)
	if err != nil {
		return nil, err
	}
	if err := m.svc.Stop(b.Name, pw); err != nil {
		return nil, err
	}
	return statusDoc(m.svc, b.Name, false)
}

type mcpDeleteResult struct {
	Bench   string `json:"bench"`
	Deleted bool   `json:"deleted"`
}

func (m *mcpServer) delete(_ context.Context, req mcp.CallToolRequest, pw *mcpProgress) (any, error) {
	b, err := m.bench(req, "bench", true)
	if err != nil {
		return nil, err
	}
	if err := m.svc.Delete(manager.DeleteInput{Name: b.Name, NoBackup: req.GetBool("no_backup", false)}, pw); err != nil {
		return nil, err
	}
	return mcpDeleteResult{Bench: b.Name, Deleted: true}, nil
}

// ─── progress ───────────────────────────────────────────────────────────────

// mcpProgress is the ProgressWriter of one tool call: each step becomes a
// progress notification when the client asked for them, and the output is
// kept (bounded) so a failure can show its last lines.
type mcpProgress struct {
	ctx   context.Context
	token mcp.ProgressToken
	n     int
	out   *tailBuffer
}

func newMCPProgress(ctx context.Context, req mcp.CallToolRequest) *mcpProgress {
	p := &mcpProgress{ctx: ctx, out: &tailBuffer{max: 16 << 10}}
	if req.Params.Meta != nil {
		p.token = req.Params.Meta.ProgressToken
	}
	return p
}

func (p *mcpProgress) Step(msg string) {
	p.n++
	fmt.Fprintf(p.out, "→ %s\n", msg)
	srv := server.ServerFromContext(p.ctx)
	if p.token == nil || srv == nil {
		return
	}
	// Best effort: a full notification channel drops it.
	_ = srv.SendNotificationToClient(p.ctx, string(mcp.MethodNotificationProgress),
		map[string]any{"progressToken": p.token, "progress": p.n, "message": msg})
}

func (p *mcpProgress) Printf(format string, args ...any) { fmt.Fprintf(p.out, format, args...) }
func (p *mcpProgress) Println(args ...any)               { fmt.Fprintln(p.out, args...) }
func (p *mcpProgress) Stderr() io.Writer                 { return p.out }

// tail is the end of the call's output, for an error message.
func (p *mcpProgress) tail() string {
	lines := strings.Split(strings.TrimSpace(p.out.String()), "\n")
	if len(lines) > 20 {
		lines = lines[len(lines)-20:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	max int
	buf bytes.Buffer
	cut bool
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf.Write(p)
	if over := t.buf.Len() - t.max; over > 0 {
		t.buf.Next(over)
		t.cut = true
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	if t.cut {
		return "[earlier output cut]\n" + t.buf.String()
	}
	return t.buf.String()
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}
