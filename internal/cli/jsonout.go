package cli

import (
	"encoding/json"
	"os"
	"time"
)

// Machine-readable output.
//
// Every --json document is one object whose "schema" field names its shape
// and version ("ffm.list/v1"). Fields are only ever added within a version;
// renaming or removing one, or changing its meaning, bumps the version and is
// listed under "Upgrade notes" in the release. Times are RFC 3339 in UTC;
// absent values are omitted rather than zero.
//
// Secrets (passwords) are omitted unless the command was given
// --show-secrets.

// writeJSON prints v as indented JSON on stdout.
func writeJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// jsonTime formats t for JSON output, or "" for the zero time.
func jsonTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

type jsonBench struct {
	Name         string `json:"name"`
	Mode         string `json:"mode"`
	DB           string `json:"db"`
	Status       string `json:"status"`
	Site         string `json:"site"`
	URL          string `json:"url"`
	WebPort      int    `json:"web_port"`
	SocketIOPort int    `json:"socketio_port"`
	Domain       string `json:"domain,omitempty"`
	ProxyHost    string `json:"proxy_host,omitempty"`
	FrappeBranch string `json:"frappe_branch"`
	Tunnel       bool   `json:"tunnel"`
}

type jsonList struct {
	Schema  string      `json:"schema"`
	Benches []jsonBench `json:"benches"`
}

type jsonStatus struct {
	Schema        string          `json:"schema"`
	Bench         jsonBench       `json:"bench"`
	Dir           string          `json:"dir"`
	FrappeRepo    string          `json:"frappe_repo,omitempty"`
	Apps          []string        `json:"apps"`
	Bind          string          `json:"bind"`
	SSHAgent      bool            `json:"ssh_agent"`
	DomainAliases []string        `json:"domain_aliases"`
	CreatedAt     string          `json:"created_at,omitempty"`
	Containers    []jsonContainer `json:"containers"`
	AdminPassword string          `json:"admin_password,omitempty"`
	DBPassword    string          `json:"db_password,omitempty"`
}

type jsonContainer struct {
	Service string `json:"service"`
	Name    string `json:"name"`
	State   string `json:"state"`
	Status  string `json:"status"`
	Health  string `json:"health,omitempty"`
}

type jsonArchive struct {
	Bench      string   `json:"bench"`
	Path       string   `json:"path"`
	Size       int64    `json:"size"`
	TakenAt    string   `json:"taken_at,omitempty"`
	Trigger    string   `json:"trigger,omitempty"`
	Contents   []string `json:"contents,omitempty"`
	Label      string   `json:"label,omitempty"`
	FfmVer     string   `json:"ffm_version,omitempty"`
	Unreadable string   `json:"unreadable,omitempty"`
}

type jsonArchives struct {
	Schema   string        `json:"schema"`
	Archives []jsonArchive `json:"archives"`
}

type jsonSchedule struct {
	Bench       string `json:"bench"`
	Enabled     bool   `json:"enabled"`
	EveryHours  int    `json:"every_hours"`
	KeepHourly  int    `json:"keep_hourly"`
	KeepDaily   int    `json:"keep_daily"`
	KeepWeekly  int    `json:"keep_weekly"`
	Files       string `json:"files"`
	Scheduled   int    `json:"scheduled_archives"`
	LastSuccess string `json:"last_success,omitempty"`
	LastFiles   string `json:"last_files,omitempty"`
	NextDue     string `json:"next_due,omitempty"`
	LastAttempt string `json:"last_attempt,omitempty"`
	LastResult  string `json:"last_result,omitempty"`
	LastError   string `json:"last_error,omitempty"`
}

type jsonSchedules struct {
	Schema    string         `json:"schema"`
	Schedules []jsonSchedule `json:"schedules"`
}

type jsonDomains struct {
	Schema  string   `json:"schema"`
	Bench   string   `json:"bench"`
	Primary string   `json:"primary"`
	Aliases []string `json:"aliases"`
	TLS     bool     `json:"alias_tls"`
}

type jsonTunnelServer struct {
	Name       string `json:"name"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	BaseDomain string `json:"base_domain"`
	TLS        bool   `json:"tls"`
	Default    bool   `json:"default"`
	Token      string `json:"token,omitempty"`
}

type jsonTunnelServers struct {
	Schema  string             `json:"schema"`
	Servers []jsonTunnelServer `json:"servers"`
}

type jsonVersion struct {
	Schema  string `json:"schema"`
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
}
