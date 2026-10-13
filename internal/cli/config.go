package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

// jsonConfigEntry is one setting in the ffm.config/v1 document. A secret-looking
// key carries no value unless the command was given --show-secrets.
type jsonConfigEntry struct {
	Key    string `json:"key"`
	Value  any    `json:"value,omitempty"`
	Secret bool   `json:"secret"`
	// Masked is true when the value was left out because the key looks secret.
	Masked bool `json:"masked,omitempty"`
}

// jsonConfig is the ffm.config/v1 document of 'ffm config get' and 'ffm config list'.
// get fills Key and Value; list fills Config.
type jsonConfig struct {
	Schema string            `json:"schema"`
	Bench  string            `json:"bench"`
	Key    string            `json:"key,omitempty"`
	Value  any               `json:"value,omitempty"`
	Secret bool              `json:"secret,omitempty"`
	Masked bool              `json:"masked,omitempty"`
	Config []jsonConfigEntry `json:"config,omitempty"`
}

// configEntryDoc turns a setting into its JSON form, hiding a secret's value
// unless show is set.
func configEntryDoc(e manager.ConfigEntry, show bool) jsonConfigEntry {
	secret := manager.IsSecretConfigKey(e.Key, e.Value)
	d := jsonConfigEntry{Key: e.Key, Secret: secret}
	if secret && !show {
		d.Masked = true
		return d
	}
	d.Value = e.Value
	return d
}

// configEntryText is the value as printed on a terminal or in a pipe: secrets
// follow the same rule as every other command (shown on a terminal or with
// --show-secrets, masked otherwise).
func configEntryText(e manager.ConfigEntry, bench string, show bool) string {
	v := manager.FormatConfigValue(e.Value)
	if manager.IsSecretConfigKey(e.Key, e.Value) {
		return manager.Secret(v, "ffm config get "+bench+" "+e.Key+" --show-secrets", show)
	}
	return v
}

// configArgs splits `[bench] key...` arguments: the last `want` arguments are
// the key (and value), anything before them is the bench.
func configArgs(args []string, want int) (bench string, rest []string, err error) {
	if len(args) < want || len(args) > want+1 {
		return "", nil, usageError{fmt.Errorf("expected %d argument(s) after the optional bench name, got %d", want, len(args))}
	}
	if len(args) == want+1 {
		return args[0], args[1:], nil
	}
	return "", args, nil
}

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Read and change a site's site_config.json: get, set, unset, list",
		Long: `Read and change the settings in the site's site_config.json (what 'bench set-config'
and 'bench show-config' work on). The bench must be running.

  ffm config list [bench]
  ffm config get [bench] <key>
  ffm config set [bench] <key> <value>
  ffm config unset [bench] <key>

Keys that look secret (password, secret, key, token, encryption) are masked
unless stdout is a terminal or --show-secrets is given. Frappe version 15 reads
site_config.json on every request; version 16 caches it for up to 60 seconds.

Keys ffm and the bench depend on (database, redis, realtime ports, the app list,
host_name on a production bench or one behind a proxy host) are refused: use the
ffm command that owns them. encryption_key is refused unless --force: changing it
makes every stored password unreadable.`,
	}
	cmd.AddCommand(newConfigListCmd(), newConfigGetCmd(), newConfigSetCmd(), newConfigUnsetCmd())
	return cmd
}

func newConfigListCmd() *cobra.Command {
	var asJSON, show bool
	cmd := &cobra.Command{
		Use:   "list [bench]",
		Short: "List the site's settings (secret values masked)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveBenchName(args, "Select a bench")
			if err != nil {
				return err
			}
			entries, err := manager.New(verbose).ConfigList(name)
			if err != nil {
				return err
			}
			if asJSON {
				doc := jsonConfig{Schema: "ffm.config/v1", Bench: name, Config: []jsonConfigEntry{}}
				for _, e := range entries {
					doc.Config = append(doc.Config, configEntryDoc(e, show))
				}
				return writeJSON(doc)
			}
			width := 0
			for _, e := range entries {
				if len(e.Key) > width {
					width = len(e.Key)
				}
			}
			for _, e := range entries {
				fmt.Printf("%-*s  %s\n", width, e.Key, configEntryText(e, name, show))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print as JSON (schema ffm.config/v1)")
	cmd.Flags().BoolVar(&show, "show-secrets", false, "Show the values of secret-looking keys even when stdout is not a terminal; with --json, include them")
	return cmd
}

func newConfigGetCmd() *cobra.Command {
	var asJSON, show bool
	cmd := &cobra.Command{
		Use:   "get [bench] <key>",
		Short: "Print one setting (a secret-looking key is masked)",
		Long: `Print the value of one setting: a string as itself, anything else as JSON.
Exits 1 when the site does not set the key.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			bench, rest, err := configArgs(args, 1)
			if err != nil {
				return err
			}
			name, err := resolveBenchName(optional(bench), "Select a bench")
			if err != nil {
				return err
			}
			e, err := manager.New(verbose).ConfigGet(name, rest[0])
			if err != nil {
				return err
			}
			if asJSON {
				d := configEntryDoc(e, show)
				return writeJSON(jsonConfig{Schema: "ffm.config/v1", Bench: name, Key: d.Key, Value: d.Value, Secret: d.Secret, Masked: d.Masked})
			}
			fmt.Println(configEntryText(e, name, show))
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print as JSON (schema ffm.config/v1)")
	cmd.Flags().BoolVar(&show, "show-secrets", false, "Show the value of a secret-looking key even when stdout is not a terminal")
	return cmd
}

func optional(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

func newConfigSetCmd() *cobra.Command {
	var asString, asJSON, fromStdin, force bool
	cmd := &cobra.Command{
		Use:   "set [bench] <key> [value]",
		Short: "Set one setting (the value's type is detected)",
		Long: `Set a site_config.json key. Without a flag the value is read as a boolean
(true, false), a number (587, 0.5), a JSON object or array, or else a string.
--string keeps the text as given ("007", "true"); --json requires valid JSON
and accepts any kind, including null and quoted strings.

A value given as an argument is visible in the process list and your shell
history. For a secret, use --stdin and pipe it in.`,
		Example: `  ffm config set mybench max_file_size 20971520
  ffm config set mybench allow_cors '["https://app.example.com"]'
  ffm config set mybench some_code 007 --string
  echo "$TOKEN" | ffm config set mybench my_api_token --stdin`,
		Args: cobra.RangeArgs(1, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if asString && asJSON {
				return usageError{errors.New("--string and --json are exclusive")}
			}
			want := 2
			if fromStdin {
				want = 1
			}
			bench, rest, err := configArgs(args, want)
			if err != nil {
				return err
			}
			raw := ""
			if fromStdin {
				if raw, err = readSecretLine(stdinReader()); err != nil {
					return err
				}
			} else {
				raw = rest[1]
			}
			mode := manager.ConfigAuto
			switch {
			case asString:
				mode = manager.ConfigString
			case asJSON:
				mode = manager.ConfigJSON
			}
			value, err := manager.ParseConfigValue(raw, mode)
			if err != nil {
				return usageError{err}
			}
			name, err := resolveBenchName(optional(bench), "Select a bench")
			if err != nil {
				return err
			}
			if err := manager.New(verbose).ConfigSet(manager.ConfigSetInput{Bench: name, Key: rest[0], Value: value, Force: force}); err != nil {
				return err
			}
			shown := manager.FormatConfigValue(value)
			if manager.IsSecretConfigKey(rest[0], value) {
				shown = manager.MaskedSecret
			}
			fmt.Printf("Set %s = %s on %q.\n", rest[0], shown, name)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asString, "string", false, "Store the value as a string, whatever it looks like")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Parse the value as JSON (any kind)")
	cmd.Flags().BoolVar(&fromStdin, "stdin", false, "Read the value from stdin instead of an argument")
	cmd.Flags().BoolVar(&force, "force", false, "Allow changing encryption_key (all stored passwords become unreadable)")
	return cmd
}

func newConfigUnsetCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "unset [bench] <key>",
		Short: "Remove one setting from the site",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			bench, rest, err := configArgs(args, 1)
			if err != nil {
				return err
			}
			name, err := resolveBenchName(optional(bench), "Select a bench")
			if err != nil {
				return err
			}
			changed, err := manager.New(verbose).ConfigUnset(name, rest[0], force)
			if err != nil {
				return err
			}
			if !changed {
				fmt.Printf("%s was not set on %q.\n", rest[0], name)
				return nil
			}
			fmt.Printf("Removed %s from %q.\n", rest[0], name)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Allow removing encryption_key (all stored passwords become unreadable)")
	return cmd
}
