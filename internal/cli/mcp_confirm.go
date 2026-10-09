package cli

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// delete asks the user through MCP elicitation before anything happens, the
// way ffc confirms destructive calls. The question is a multi round-trip
// input request: the client asks its user and calls the tool again with the
// answer and the request state ffm issued, which binds the answer to this
// tool and its arguments, expires, and can be used once. A client that cannot
// ask gets a refusal naming the terminal command instead.
//
// Confirmation trusts the client to have shown the question to a person.

const (
	confirmID  = "confirm"
	confirmTTL = 10 * time.Minute
)

type confirmer struct {
	key  []byte
	mu   sync.Mutex
	used map[string]time.Time
	now  func() time.Time
}

var mcpConfirm = newConfirmer()

func newConfirmer() *confirmer {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(fmt.Sprintf("reading random bytes: %v", err))
	}
	return &confirmer{key: key, used: map[string]time.Time{}, now: time.Now}
}

func (c *confirmer) newState(req mcp.CallToolRequest) string {
	nonce := make([]byte, 12)
	_, _ = rand.Read(nonce)
	exp := c.now().Add(confirmTTL).Unix()
	return c.sign(req, strconv.FormatInt(exp, 10)+"."+base64.RawURLEncoding.EncodeToString(nonce))
}

// sign binds head (expiry and nonce) to the tool and its arguments.
func (c *confirmer) sign(req mcp.CallToolRequest, head string) string {
	args, _ := json.Marshal(req.GetArguments()) // map keys are sorted
	mac := hmac.New(sha256.New, c.key)
	fmt.Fprintf(mac, "%q %q ", req.Params.Name, head)
	mac.Write(args)
	return head + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// spend reports whether the call carries a state issued for exactly this
// call, unexpired and unused, and marks it used.
func (c *confirmer) spend(req mcp.CallToolRequest) bool {
	st := req.Params.RequestState
	i := strings.LastIndexByte(st, '.')
	if i < 0 {
		return false
	}
	head := st[:i]
	sec, _, _ := strings.Cut(head, ".")
	unix, err := strconv.ParseInt(sec, 10, 64)
	if err != nil {
		return false
	}
	now, exp := c.now(), time.Unix(unix, 0)
	if !now.Before(exp) || !hmac.Equal([]byte(st), []byte(c.sign(req, head))) {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.used {
		if !now.Before(e) {
			delete(c.used, k)
		}
	}
	if _, spent := c.used[st]; spent {
		return false
	}
	c.used[st] = exp
	return true
}

var confirmSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"confirm": map[string]any{
			"type": "boolean", "title": "Confirm", "default": false,
			"description": "Check to go ahead.",
		},
	},
	"required": []string{"confirm"},
}

// confirm returns nil to let the call go ahead, or the result to send
// instead: the question, a refusal, or the user's "no".
func (c *confirmer) confirm(ctx context.Context, req mcp.CallToolRequest) *mcp.CallToolResult {
	if answer := server.ElicitationResponse(req.Params.InputResponses, confirmID); answer != nil && c.spend(req) {
		if answer.Action == mcp.ElicitationResponseActionAccept && confirmed(answer.Content) {
			return nil
		}
		return mcp.NewToolResultError("cancelled by the user; nothing was changed")
	}
	bench := req.GetString("bench", "")
	if !canElicit(ctx) {
		return mcp.NewToolResultError(fmt.Sprintf("policy: %s needs the user's confirmation and this MCP client cannot ask for it; nothing was changed. Run it in a terminal instead: ffm delete %s",
			req.Params.Name, bench))
	}
	msg := fmt.Sprintf("Delete the bench %q? Its containers, volumes (the site's database) and directory are removed.", bench)
	if req.GetBool("no_backup", false) {
		msg += " No backup is taken first."
	} else {
		msg += " A backup is taken first."
	}
	return server.NewInputRequestBuilder(c.newState(req)).Elicit(confirmID, mcp.ElicitationParams{
		Message:         msg,
		RequestedSchema: confirmSchema,
	}).ToolResult()
}

// canElicit reports whether the client said it can ask its user a form
// question: per request on the 2026-07-28 protocol, at initialize before.
func canElicit(ctx context.Context) bool {
	var caps *mcp.ClientCapabilities
	if info := server.RequestProtocolInfoFromContext(ctx); info != nil && info.Modern {
		caps = info.ClientCapabilities
	} else if s, ok := server.ClientSessionFromContext(ctx).(server.SessionWithClientInfo); ok {
		if _, ok := s.(server.SessionWithElicitation); ok {
			c := s.GetClientCapabilities()
			caps = &c
		}
	}
	if caps == nil || caps.Elicitation == nil {
		return false
	}
	// "elicitation": {} means form mode only.
	return caps.Elicitation.Form != nil || caps.Elicitation.URL == nil
}

func confirmed(content any) bool {
	m, _ := content.(map[string]any)
	yes, _ := m["confirm"].(bool)
	return yes
}
