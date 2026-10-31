package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/orshih6/pikactl/internal/config"
	"github.com/orshih6/pikactl/internal/output"
)

func newAuthCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "auth", Short: "Store, check and remove the API key"}
	cmd.AddCommand(newAuthLoginCommand(), newAuthStatusCommand(), newAuthLogoutCommand())
	return cmd
}

func newAuthLoginCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Store an API key in the config file",
		Long: "Store an API key in the config file (mode 0600). Create the key at\n" +
			"https://dev.pika.art/keys. It is read from stdin, so it never appears in the\n" +
			"shell history, and it is checked against the API before it is saved.\n\n" +
			"Setting " + config.KeyVar + ", in the environment or a .env file, works as well.",
		Example: `  pikactl auth login
  pbpaste | pikactl auth login`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			key, err := readSecret(cmd, "API key: ")
			if err != nil {
				return err
			}
			if key == "" {
				return errors.New("no key given, nothing was saved")
			}
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			a.api.Key = key
			balance, err := a.readBalance(cmd.Context())
			if err != nil {
				return fmt.Errorf("the key did not work, nothing was saved: %w", err)
			}
			fc, err := config.Load(a.cfg.Path)
			if err != nil {
				return err
			}
			fc.APIKey = key
			if err := config.Save(a.cfg.Path, fc); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Key saved to %s\nBalance: %s\n", a.cfg.Path, output.USD(balance.Available()))
			if os.Getenv(config.KeyVar) != "" {
				a.logf("note: %s is set and takes precedence over the saved key", config.KeyVar)
			}
			return nil
		},
	}
}

func readSecret(cmd *cobra.Command, prompt string) (string, error) {
	in := cmd.InOrStdin()
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(cmd.ErrOrStderr(), prompt)
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(cmd.ErrOrStderr())
		return strings.TrimSpace(string(b)), err
	}
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func newAuthStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show where the key comes from and whether it works",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			if err := a.cfg.RequireKey(); err != nil {
				return err
			}
			balance, err := a.readBalance(cmd.Context())
			if err != nil {
				return err
			}
			return emit(a.out, map[string]any{"key_source": a.cfg.KeySource, "balance_micro_usd": balance.Available()}, func() error {
				return output.Fields(a.out, []output.Field{
					{Name: "key from", Value: a.cfg.KeySource},
					{Name: "key", Value: "works"},
					{Name: "balance", Value: output.USD(balance.Available())},
				})
			})
		},
	}
}

func newAuthLogoutCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Remove the saved key from the config file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fc, err := config.Load(flagConfigPath)
			if err != nil {
				return err
			}
			fc.APIKey = ""
			if err := config.Save(flagConfigPath, fc); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed the key from %s\n", flagConfigPath)
			if cfg, err := config.Resolve(flagConfigPath); err == nil && cfg.Key != "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "note: a key is still set in %s\n", cfg.KeySource)
			}
			return nil
		},
	}
}
