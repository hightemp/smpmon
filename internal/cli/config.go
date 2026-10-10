package cli

import (
	"fmt"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"

	"github.com/hightemp/smpmon/internal/config"
)

// knownWidget validates widget names in config; set by the UI package once
// widgets exist. Until then every name is accepted.
var knownWidget func(string) bool

// resolveConfigPath returns the config path and whether it was given
// explicitly (an explicit path must exist).
func resolveConfigPath(flag string) (string, bool, error) {
	if flag != "" {
		return flag, true, nil
	}
	p, err := config.DefaultPath()
	return p, false, err
}

func loadConfig(flag string) (*config.Config, string, error) {
	path, explicit, err := resolveConfigPath(flag)
	if err != nil {
		return nil, "", err
	}
	cfg, err := config.Load(path, !explicit)
	if err != nil {
		return nil, path, err
	}
	if err := cfg.Validate(knownWidget); err != nil {
		return nil, path, fmt.Errorf("%s: invalid config:\n%w", path, err)
	}
	return cfg, path, nil
}

func newConfigCommand(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect and validate the configuration",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "path",
			Short: "Print the config file path",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				p, _, err := resolveConfigPath(opts.configPath)
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(cmd.OutOrStdout(), p)
				return err
			},
		},
		&cobra.Command{
			Use:   "default",
			Short: "Print the built-in default config (a starting point for your own)",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				_, err := cmd.OutOrStdout().Write(config.DefaultYAML())
				return err
			},
		},
		&cobra.Command{
			Use:   "validate [file]",
			Short: "Check a config file for errors",
			Args:  cobra.MaximumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				flag := opts.configPath
				if len(args) == 1 {
					flag = args[0]
				}
				_, path, err := loadConfig(flag)
				if err != nil {
					return err
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s: OK\n", path)
				return err
			},
		},
		&cobra.Command{
			Use:   "print",
			Short: "Print the effective config (defaults merged with your file)",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				cfg, _, err := loadConfig(opts.configPath)
				if err != nil {
					return err
				}
				enc := yaml.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent(2)
				if err := enc.Encode(cfg); err != nil {
					return err
				}
				return enc.Close()
			},
		},
	)
	return cmd
}
