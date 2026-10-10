// Package cli defines the smpmon command-line interface.
package cli

import (
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/spf13/cobra"

	"github.com/hightemp/smpmon/internal/buildinfo"
	"github.com/hightemp/smpmon/internal/logging"
)

type rootOptions struct {
	configPath string
	dashboard  string
	logPath    string
	debug      bool
}

// NewRootCommand builds the root command with all subcommands attached.
func NewRootCommand() *cobra.Command {
	opts := &rootOptions{}
	var logCloser io.Closer

	cmd := &cobra.Command{
		Use:           "smpmon",
		Short:         "A configurable terminal resource monitor",
		Long:          "smpmon is a terminal resource monitor with configurable dashboards and graphs.",
		Version:       buildinfo.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			path := opts.logPath
			if path == "" {
				p, err := logging.DefaultPath()
				if err != nil {
					return err
				}
				path = p
			}
			c, err := logging.Setup(path, opts.debug)
			if err != nil {
				return err
			}
			logCloser = c
			slog.Debug("starting", "version", buildinfo.Version, "command", cmd.CommandPath())
			return nil
		},
		PersistentPostRunE: func(*cobra.Command, []string) error {
			if logCloser != nil {
				return logCloser.Close()
			}
			return nil
		},
		RunE: func(*cobra.Command, []string) error {
			return errors.New("TUI is not implemented yet")
		},
	}
	cmd.SetVersionTemplate("{{.Version}}\n")

	f := cmd.PersistentFlags()
	f.StringVarP(&opts.configPath, "config", "c", "", "path to config file (default: <user config dir>/smpmon/config.yaml)")
	f.StringVar(&opts.logPath, "log-file", "", "path to log file (default: <user cache dir>/smpmon/smpmon.log)")
	f.BoolVar(&opts.debug, "debug", false, "enable debug logging")
	cmd.Flags().StringVarP(&opts.dashboard, "dashboard", "d", "", "dashboard to open on start")

	cmd.AddCommand(newVersionCommand())
	return cmd
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), buildinfo.String())
			return err
		},
	}
}
