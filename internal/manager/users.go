package manager

import (
	"encoding/json"
	"fmt"
	"net/mail"
	"strings"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// userScript lists, creates, enables, disables and re-passwords Frappe users.
//
// It goes through the User doctype (not `bench add-user` or `set-password`):
// both take the password as an argument, which bench passes on to the frappe
// interpreter's argument list, and the doctype is what applies Frappe's own
// password-strength policy and session handling. The request, password
// included, arrives on stdin.
const userScript = `import json, sys
import frappe
req = json.load(sys.stdin)
frappe.init(site=sys.argv[1], sites_path=".")
frappe.connect()
frappe.set_user("Administrator")
op = req["op"]
res = {}
if op == "list":
    users = frappe.get_all("User", filters={"name": ["not in", ["Guest"]]},
        fields=["name", "email", "full_name", "enabled", "user_type", "last_login"], order_by="creation asc")
    roles = {}
    for r in frappe.get_all("Has Role", filters={"parenttype": "User"}, fields=["parent", "role"]):
        roles.setdefault(r.parent, []).append(r.role)
    res["users"] = [dict(u, roles=sorted(roles.get(u.name, []))) for u in users]
elif op == "add":
    email = req["email"]
    if frappe.db.exists("User", email):
        sys.exit("user %s already exists" % email)
    user = frappe.new_doc("User")
    user.update({"email": email, "first_name": req["first_name"], "last_name": req.get("last_name") or None,
        "enabled": 1, "user_type": "System User", "send_welcome_email": 0, "new_password": req["password"]})
    user.insert(ignore_permissions=True)
    if req.get("system_manager"):
        user.add_roles("System Manager")
    frappe.db.commit()
    res["name"] = user.name
else:
    name = req["name"]
    if not frappe.db.exists("User", name):
        sys.exit("user %s does not exist" % name)
    user = frappe.get_doc("User", name)
    if op in ("enable", "disable"):
        user.enabled = 1 if op == "enable" else 0
    elif op == "password":
        user.new_password = req["password"]
        user.logout_all_sessions = 1
    else:
        sys.exit("unknown operation %s" % op)
    user.save(ignore_permissions=True)
    frappe.db.commit()
    res["name"] = user.name
print("` + resultPrefix + `" + json.dumps(res, default=str))
`

// UserInfo is one row of `ffm user list`.
type UserInfo struct {
	Name      string   `json:"name"`
	Email     string   `json:"email"`
	FullName  string   `json:"full_name"`
	Enabled   bool     `json:"enabled"`
	UserType  string   `json:"user_type"`
	LastLogin string   `json:"last_login,omitempty"`
	Roles     []string `json:"roles"`
	// SystemManager is true when the user holds the System Manager role.
	SystemManager bool `json:"system_manager"`
}

type userRequest struct {
	Op            string `json:"op"`
	Name          string `json:"name,omitempty"`
	Email         string `json:"email,omitempty"`
	FirstName     string `json:"first_name,omitempty"`
	LastName      string `json:"last_name,omitempty"`
	SystemManager bool   `json:"system_manager,omitempty"`
	Password      string `json:"password,omitempty"`
}

func (s *Service) userCall(b state.Bench, req userRequest) (json.RawMessage, error) {
	var secrets []string
	if req.Password != "" {
		secrets = append(secrets, req.Password)
	}
	raw, err := runSiteRequest(s.quietRunnerFor(b), b.SiteName, userScript, req, secrets...)
	if err != nil {
		return nil, fmt.Errorf("user %s: %w", req.Op, err)
	}
	return raw, nil
}

// UserList returns the site's users except Guest. The bench must be running.
func (s *Service) UserList(name string) ([]UserInfo, error) {
	b, err := s.runningBench(name)
	if err != nil {
		return nil, err
	}
	raw, err := s.userCall(b, userRequest{Op: "list"})
	if err != nil {
		return nil, err
	}
	var res struct {
		Users []UserInfo `json:"users"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("user list: unreadable result: %w", err)
	}
	for i := range res.Users {
		for _, r := range res.Users[i].Roles {
			if r == "System Manager" {
				res.Users[i].SystemManager = true
			}
		}
		if res.Users[i].Roles == nil {
			res.Users[i].Roles = []string{}
		}
	}
	return res.Users, nil
}

// UserAddInput describes `ffm user add`.
type UserAddInput struct {
	Bench         string
	Email         string
	FirstName     string
	LastName      string
	SystemManager bool
	// Password is required: without one Frappe emails a set-password link, and
	// a bench rarely has working outgoing mail. It never reaches an argument list.
	Password string
}

// ValidateUserEmail checks that s is a bare address (no display name).
func ValidateUserEmail(s string) error {
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s || strings.ContainsAny(s, " \t,;<>") {
		return fmt.Errorf("%q is not an email address", s)
	}
	return nil
}

// UserAdd creates an enabled System User with the given password. Frappe's
// password-strength policy applies to it as it does in the desk.
func (s *Service) UserAdd(in UserAddInput) error {
	if err := ValidateUserEmail(in.Email); err != nil {
		return err
	}
	if err := singleLine("the password", in.Password); err != nil {
		return err
	}
	if strings.TrimSpace(in.FirstName) == "" {
		in.FirstName = strings.Split(in.Email, "@")[0]
	}
	release, err := s.lockBench(in.Bench)
	if err != nil {
		return err
	}
	defer release()
	b, err := s.runningBench(in.Bench)
	if err != nil {
		return err
	}
	_, err = s.userCall(b, userRequest{Op: "add", Email: in.Email, FirstName: in.FirstName,
		LastName: in.LastName, SystemManager: in.SystemManager, Password: in.Password})
	return err
}

// normalizeUser spells Administrator (and Guest) the way Frappe does, so
// "administrator" cannot slip past the checks on the real name.
func normalizeUser(u string) string {
	u = strings.TrimSpace(u)
	for _, n := range []string{"Administrator", "Guest"} {
		if strings.EqualFold(u, n) {
			return n
		}
	}
	return u
}

// UserSetEnabled enables or disables a user. Administrator and Guest cannot be
// disabled, and neither can the agent user of an agent-ready bench: ffc and the
// MCP server act as it, and 'ffm agent off' is the way to retire it.
func (s *Service) UserSetEnabled(name, user string, enabled bool) error {
	user = normalizeUser(user)
	release, err := s.lockBench(name)
	if err != nil {
		return err
	}
	defer release()
	b, err := s.runningBench(name)
	if err != nil {
		return err
	}
	if !enabled {
		switch {
		case user == "Administrator" || user == "Guest":
			return &PreflightError{msg: fmt.Sprintf("refusing to disable %s: the site cannot run without it", user)}
		case b.Agent && strings.EqualFold(user, agentEmail(b)):
			return &PreflightError{msg: fmt.Sprintf("refusing to disable %s: it is bench %q's agent user; use 'ffm agent off'", user, b.Name)}
		}
	}
	op := "enable"
	if !enabled {
		op = "disable"
	}
	_, err = s.userCall(b, userRequest{Op: op, Name: user})
	return err
}

// UserSetPassword changes a user's password and signs the user out everywhere.
//
// Administrator goes through `bench set-admin-password` (what create, restore
// and rotation use, with no strength policy, so a dev bench can keep a short
// one) and ffm's record of the Administrator password is updated with it, the
// way rotate-credentials does; any other user goes through the User doctype
// and Frappe's password policy. The password is not stored for anyone else.
func (s *Service) UserSetPassword(name, user, password string) error {
	user = normalizeUser(user)
	if err := singleLine("the password", password); err != nil {
		return err
	}
	release, err := s.lockBench(name)
	if err != nil {
		return err
	}
	defer release()
	b, err := s.runningBench(name)
	if err != nil {
		return err
	}
	if user == "Administrator" {
		if err := bench.ValidateAdminPassword(password); err != nil {
			return err
		}
		runner := s.runnerFor(b)
		runner.Redact = append(runner.Redact, password)
		if err := setAdminPassword(runner, b.SiteName, password); err != nil {
			return fmt.Errorf("set the Administrator password: %w", err)
		}
		if err := s.UpdateBench(b.Name, func(rec *state.Bench) { rec.AdminPassword = password }); err != nil {
			// Frappe has it, ffm's record does not: `ffm status` would show a
			// stale value. The caller supplied the password, so nothing is lost.
			return fmt.Errorf("the Administrator password was changed but ffm could not record it: %w", err)
		}
		return nil
	}
	_, err = s.userCall(b, userRequest{Op: "password", Name: user, Password: password})
	return err
}
