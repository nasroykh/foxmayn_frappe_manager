package manager

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// ErrConfigKeyNotFound is returned by ConfigGet for a key the site does not set.
var ErrConfigKeyNotFound = errors.New("the site does not set that key")

// ConfigValueMode says how ConfigSet reads the value it is given.
type ConfigValueMode int

const (
	// ConfigAuto reads true/false as a boolean, an integer or a decimal as a
	// number, a JSON object or array as itself, and anything else as a string.
	ConfigAuto ConfigValueMode = iota
	// ConfigString keeps the text exactly as given ("007", "true").
	ConfigString
	// ConfigJSON requires the text to be a JSON document of any kind.
	ConfigJSON
)

var (
	intRe   = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)
	floatRe = regexp.MustCompile(`^-?(0|[1-9][0-9]*)\.[0-9]+$`)
	keyRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,127}$`)
)

// ParseConfigValue turns the text of a command line into the value stored in
// site_config.json. Numbers keep their exact text (json.Number), so a large
// integer is not rounded through a float on its way to Frappe.
func ParseConfigValue(raw string, mode ConfigValueMode) (any, error) {
	switch mode {
	case ConfigString:
		return raw, nil
	case ConfigJSON:
		v, err := decodeJSON(raw)
		if err != nil {
			return nil, fmt.Errorf("--json: %q is not valid JSON: %w", truncate(raw, 40), err)
		}
		return v, nil
	}
	switch strings.ToLower(raw) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	if intRe.MatchString(raw) || floatRe.MatchString(raw) {
		return json.Number(raw), nil
	}
	if t := strings.TrimSpace(raw); strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
		if v, err := decodeJSON(t); err == nil {
			return v, nil
		}
	}
	return raw, nil
}

func decodeJSON(raw string) (any, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("trailing data after the document")
	}
	return v, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// FormatConfigValue is how a value is printed: a string as itself, anything
// else as compact JSON.
func FormatConfigValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Sprint(v)
	}
	return strings.TrimSpace(buf.String())
}

var (
	secretKeyWords = []string{"password", "passwd", "secret", "key", "token", "encryption"}
	urlCredsRe     = regexp.MustCompile(`://[^/@\s]*:[^/@\s]+@`)
)

// IsSecretConfigKey reports whether a site_config entry should be masked in
// output: a key that names a password, secret, key, token or encryption
// setting, or a URL value that carries credentials (redis://:pw@host).
func IsSecretConfigKey(key string, value any) bool {
	k := strings.ToLower(key)
	for _, w := range secretKeyWords {
		if strings.Contains(k, w) {
			return true
		}
	}
	if s, ok := value.(string); ok && urlCredsRe.MatchString(s) {
		return true
	}
	return false
}

// ConfigEntry is one site_config.json setting.
type ConfigEntry struct {
	Key   string
	Value any
}

// configOwnedKeys are keys ffm or the bench wiring depend on. A change breaks
// the site (the database connection, the redis queues, realtime) or is undone
// by the next ffm operation, so set and unset refuse them and name the command
// that owns the setting. They are Create's `set-config` calls plus the database
// credentials Frappe wrote at new-site; Restore drops the same ones from an
// archive's configuration (siteConfigOwnedKeys).
var configOwnedKeys = map[string]string{
	"db_name":             "the site's database",
	"db_user":             "the site's database",
	"db_password":         "the site's database",
	"db_type":             "the database engine",
	"db_host":             "the database container",
	"db_port":             "the database container",
	"db_schema":           "the database schema",
	"db_socket":           "the database connection",
	"redis_cache":         "the bench's redis cache",
	"redis_queue":         "the bench's redis queue",
	"redis_socketio":      "the bench's realtime queue",
	"socketio_port":       "realtime; 'ffm set-proxy' sets it",
	"socketio_frappe_url": "realtime inside the bench network",
	"webserver_port":      "the web server's port",
	"installed_apps":      "the app list; 'ffm app add|remove' keep it",
}

// configGuard returns the reason set/unset must not touch key on this bench,
// or nil. force only lifts the encryption_key rule.
func configGuard(b state.Bench, key string, force bool) error {
	if !keyRe.MatchString(key) {
		return fmt.Errorf("invalid key %q: letters, digits, '_', '.' and '-', starting with a letter or '_'", key)
	}
	if what, ok := configOwnedKeys[key]; ok {
		return &PreflightError{msg: fmt.Sprintf("refusing to change %q: it holds %s and ffm depends on it", key, what)}
	}
	if key == "host_name" && (b.IsProd() || b.ProxyHost != "") {
		return &PreflightError{msg: fmt.Sprintf("refusing to change \"host_name\" on bench %q: ffm sets it from the "+
			"domain and proxy host; change those with 'ffm set-proxy' or 'ffm domain'", b.Name)}
	}
	if key == "encryption_key" && !force {
		return &PreflightError{msg: "refusing to change \"encryption_key\": every Password field and stored credential " +
			"in the database is encrypted with it, and a new key makes them unreadable (pass --force if you mean it)"}
	}
	return nil
}

// configScript reads or writes sites/<site>/site_config.json. It does not import
// frappe: it needs no database, and it writes the file the way Frappe does
// (indent 1, sorted keys), through a temporary file so a reader never sees half
// of it. The request arrives on stdin.
const configScript = `site = sys.argv[1]
req = json.load(sys.stdin)
path = os.path.join(site, "site_config.json")
with open(path) as f:
    cfg = json.load(f)
op, key = req["op"], req.get("key")
res = {}
if op == "list":
    res["config"] = cfg
elif op == "get":
    res["found"] = key in cfg
    res["value"] = cfg.get(key)
else:
    if op == "set":
        cfg[key] = req["value"]
        res["changed"] = True
    else:
        res["changed"] = key in cfg
        cfg.pop(key, None)
    if res["changed"]:
        tmp = path + ".ffm-tmp"
        mode = os.stat(path).st_mode & 0o777
        with open(tmp, "w") as f:
            json.dump(cfg, f, indent=1, sort_keys=True)
            f.write("\n")
        os.chmod(tmp, mode)
        os.replace(tmp, path)
print("` + resultPrefix + `" + json.dumps(res))
`

type configRequest struct {
	Op    string `json:"op"`
	Key   string `json:"key,omitempty"`
	Value any    `json:"value"`
}

func (s *Service) configCall(b state.Bench, req configRequest, secrets ...string) (map[string]json.RawMessage, error) {
	raw, err := runSiteRequest(s.quietRunnerFor(b), b.SiteName, configScript, req, secrets...)
	if err != nil {
		return nil, fmt.Errorf("site_config %s: %w", req.Op, err)
	}
	var res map[string]json.RawMessage
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("site_config %s: unreadable result: %w", req.Op, err)
	}
	return res, nil
}

func decodeRaw(raw json.RawMessage) (any, error) {
	return decodeJSON(string(raw))
}

// ConfigList returns every setting of the site's site_config.json, sorted by
// key, values UNMASKED: the caller decides what to show. The bench must be running.
func (s *Service) ConfigList(name string) ([]ConfigEntry, error) {
	b, err := s.runningBench(name)
	if err != nil {
		return nil, err
	}
	res, err := s.configCall(b, configRequest{Op: "list"})
	if err != nil {
		return nil, err
	}
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(res["config"], &cfg); err != nil {
		return nil, fmt.Errorf("site_config list: %w", err)
	}
	out := make([]ConfigEntry, 0, len(cfg))
	for k, raw := range cfg {
		v, err := decodeRaw(raw)
		if err != nil {
			return nil, fmt.Errorf("site_config key %q: %w", k, err)
		}
		out = append(out, ConfigEntry{Key: k, Value: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// ConfigGet returns one setting, unmasked, or ErrConfigKeyNotFound.
func (s *Service) ConfigGet(name, key string) (ConfigEntry, error) {
	if !keyRe.MatchString(key) {
		return ConfigEntry{}, fmt.Errorf("invalid key %q", key)
	}
	b, err := s.runningBench(name)
	if err != nil {
		return ConfigEntry{}, err
	}
	res, err := s.configCall(b, configRequest{Op: "get", Key: key})
	if err != nil {
		return ConfigEntry{}, err
	}
	if string(res["found"]) != "true" {
		return ConfigEntry{}, fmt.Errorf("%w: %s", ErrConfigKeyNotFound, key)
	}
	v, err := decodeRaw(res["value"])
	if err != nil {
		return ConfigEntry{}, err
	}
	return ConfigEntry{Key: key, Value: v}, nil
}

// ConfigSetInput is one `ffm config set`.
type ConfigSetInput struct {
	Bench string
	Key   string
	Value any
	// Force lifts the encryption_key rule (and nothing else).
	Force bool
}

// ConfigSet writes one setting. It holds the bench lock, so it cannot land in
// the middle of a backup, update or restore. Frappe reads site_config.json per
// request on version 15 and caches it for 60 seconds on version 16.
func (s *Service) ConfigSet(in ConfigSetInput) error {
	release, err := s.lockBench(in.Bench)
	if err != nil {
		return err
	}
	defer release()
	b, err := s.runningBench(in.Bench)
	if err != nil {
		return err
	}
	if err := configGuard(b, in.Key, in.Force); err != nil {
		return err
	}
	var secrets []string
	if str, ok := in.Value.(string); ok && IsSecretConfigKey(in.Key, in.Value) {
		secrets = append(secrets, str)
	}
	_, err = s.configCall(b, configRequest{Op: "set", Key: in.Key, Value: in.Value}, secrets...)
	return err
}

// ConfigUnset removes one setting and reports whether it was set.
func (s *Service) ConfigUnset(name, key string, force bool) (bool, error) {
	release, err := s.lockBench(name)
	if err != nil {
		return false, err
	}
	defer release()
	b, err := s.runningBench(name)
	if err != nil {
		return false, err
	}
	if err := configGuard(b, key, force); err != nil {
		return false, err
	}
	res, err := s.configCall(b, configRequest{Op: "unset", Key: key})
	if err != nil {
		return false, err
	}
	return string(res["changed"]) == "true", nil
}
