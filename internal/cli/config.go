package cli

import (
	"fmt"
	"log/slog"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"

	"github.com/hightemp/smpmon/internal/config"
	"github.com/hightemp/smpmon/internal/ui"
)

// resolveConfigPath returns the config path and whether it was given
// explicitly (an explicit path must exist).
func resolveConfigPath(flag string) (string, bool, error) {
	if flag != "" {
		return flag, true, nil
	}
	p, err := config.DefaultPath()
	return p, false, err
}

// loadThemes loads user theme files. Errors are returned for reporting, but
// callers may continue: only the selected theme has to be valid.
func loadThemes() error {
	dir, err := ui.ThemesDir()
	if err != nil {
		return err
	}
	return ui.LoadUserThemes(dir)
}

func loadConfig(flag string) (*config.Config, string, error) {
	if err := loadThemes(); err != nil {
		slog.Warn("theme files", "err", err)
	}
	path, explicit, err := resolveConfigPath(flag)
	if err != nil {
		return nil, "", err
	}
	cfg, err := config.Load(path, !explicit)
	if err != nil {
		return nil, path, err
	}
	if err := ui.CheckConfig(cfg); err != nil {
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
				if err := loadThemes(); err != nil {
					return fmt.Errorf("theme files:\n%w", err)
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

func newThemeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "theme",
		Short: "List and export color themes",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "List built-in and user themes",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				if err := loadThemes(); err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "warning:", err)
				}
				for _, n := range ui.ThemeNames() {
					if _, err := fmt.Fprintln(cmd.OutOrStdout(), n); err != nil {
						return err
					}
				}
				return nil
			},
		},
		&cobra.Command{
			Use:   "export <name>",
			Short: "Print a theme as YAML, a starting point for your own",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				_ = loadThemes()
				t, err := ui.LookupTheme(args[0])
				if err != nil {
					return err
				}
				data, err := ui.ExportTheme(t)
				if err != nil {
					return err
				}
				_, err = cmd.OutOrStdout().Write(data)
				return err
			},
		},
		&cobra.Command{
			Use:   "path",
			Short: "Print the directory for user themes",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				dir, err := ui.ThemesDir()
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(cmd.OutOrStdout(), dir)
				return err
			},
		},
	)
	return cmd
}
