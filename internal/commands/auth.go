package commands

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/ziyan/cf/internal/client"
	"github.com/ziyan/cf/internal/config"
	"github.com/ziyan/cf/internal/printer"
	"golang.org/x/term"
)

func init() {
	authCommand := &cobra.Command{
		Use:   "auth",
		Short: "Manage the sites this talks to",
	}

	loginCommand := &cobra.Command{
		Use:   "login",
		Short: "Record a site and an API token",
		Long: "An API token comes from id.atlassian.com, under Security, API tokens. It is " +
			"not your password, and it can be revoked on its own.\n\n" +
			"The token is read from the terminal without echoing it, or from CF_TOKEN when " +
			"there is no terminal to ask.",
		Args: cobra.NoArgs,
		RunE: authLoginRun,
	}
	loginCommand.Flags().String("domain", "", "The site, such as example.atlassian.net")
	loginCommand.Flags().String("email", "", "The account the token belongs to")
	loginCommand.Flags().String("name", "default", "A name for this profile")

	listCommand := &cobra.Command{
		Use:   "list",
		Short: "Show the profiles that are recorded",
		Args:  cobra.NoArgs,
		RunE:  authListRun,
	}

	useCommand := &cobra.Command{
		Use:   "use <name>",
		Short: "Make a profile the active one",
		Args:  cobra.ExactArgs(1),
		RunE:  authUseRun,
	}

	removeCommand := &cobra.Command{
		Use:   "remove <name>",
		Short: "Forget a profile",
		Args:  cobra.ExactArgs(1),
		RunE:  authRemoveRun,
	}

	authCommand.AddCommand(loginCommand, listCommand, useCommand, removeCommand)
	rootCommand.AddCommand(authCommand)
}

func authLoginRun(command *cobra.Command, _ []string) error {
	domain, _ := command.Flags().GetString("domain")
	email, _ := command.Flags().GetString("email")
	name, _ := command.Flags().GetString("name")

	reader := bufio.NewReader(os.Stdin)
	var err error
	if domain, err = askFor(reader, "Site", domain); err != nil {
		return err
	}
	// A pasted URL is what somebody has to hand, and only the host matters.
	domain = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(domain, "https://"), "http://"), "/")
	if index := strings.Index(domain, "/"); index > 0 {
		domain = domain[:index]
	}
	if email, err = askFor(reader, "Email", email); err != nil {
		return err
	}

	token := os.Getenv("CF_TOKEN")
	if token == "" {
		token, err = askForSecret("API token (from id.atlassian.com)")
		if err != nil {
			return err
		}
	}
	if domain == "" || email == "" || token == "" {
		return fmt.Errorf("commands: a site, an email and a token are all needed")
	}

	profile := &config.Profile{Domain: domain, Email: email, Token: token}

	// The credentials are checked before they are written, so a typo is found
	// here rather than on the first sync.
	printer.PrintInfo("checking...")
	apiClient := client.New(profile)
	who := struct {
		DisplayName string `json:"displayName"`
		Email       string `json:"email"`
	}{}
	if err := apiClient.Get(context.Background(), "/wiki/rest/api/user/current", &who); err != nil {
		return fmt.Errorf("commands: those credentials were refused: %w", err)
	}

	configuration, err := config.LoadOrEmpty()
	if err != nil {
		return err
	}
	configuration.Profiles[name] = profile
	configuration.ActiveProfile = name
	if err := configuration.Save(); err != nil {
		return err
	}
	path, _ := config.Path()
	printer.PrintSuccess("signed in to %s as %s, saved as %q in %s",
		domain, displayOr(who.DisplayName, email), name, path)
	return nil
}

func authListRun(command *cobra.Command, _ []string) error {
	configuration, err := config.Load()
	if err != nil {
		return err
	}
	names := make([]string, 0, len(configuration.Profiles))
	for name := range configuration.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)

	if printer.JSONOutput {
		printer.PrintJSON(map[string]interface{}{
			"active":   configuration.ActiveProfile,
			"profiles": names,
		})
		return nil
	}
	if len(names) == 0 {
		printer.PrintInfo("No profiles. Run cf auth login.")
		return nil
	}
	for _, name := range names {
		marker := "  "
		if name == configuration.ActiveProfile {
			marker = "* "
		}
		profile := configuration.Profiles[name]
		printer.PrintInfo("%s%-12s %s  %s", marker, name, profile.Domain, profile.Email)
	}
	return nil
}

func authUseRun(_ *cobra.Command, arguments []string) error {
	configuration, err := config.Load()
	if err != nil {
		return err
	}
	if _, isKnown := configuration.Profiles[arguments[0]]; !isKnown {
		return fmt.Errorf("commands: no profile named %q", arguments[0])
	}
	configuration.ActiveProfile = arguments[0]
	if err := configuration.Save(); err != nil {
		return err
	}
	printer.PrintSuccess("%s is now the active profile", arguments[0])
	return nil
}

func authRemoveRun(_ *cobra.Command, arguments []string) error {
	configuration, err := config.Load()
	if err != nil {
		return err
	}
	if err := configuration.Remove(arguments[0]); err != nil {
		return err
	}
	if err := configuration.Save(); err != nil {
		return err
	}
	printer.PrintSuccess("%s is forgotten", arguments[0])
	return nil
}

func askFor(reader *bufio.Reader, label, given string) (string, error) {
	if given != "" {
		return given, nil
	}
	_, _ = fmt.Fprintf(printer.Stdout, "%s: ", label)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("commands: reading %s: %w", strings.ToLower(label), err)
	}
	return strings.TrimSpace(line), nil
}

// askForSecret reads without echoing, so a token does not end up in a
// screenshot or a scrollback buffer.
func askForSecret(label string) (string, error) {
	descriptor := int(os.Stdin.Fd())
	if !term.IsTerminal(descriptor) {
		return "", fmt.Errorf("commands: no terminal to ask for the token, set CF_TOKEN instead")
	}
	_, _ = fmt.Fprintf(printer.Stdout, "%s: ", label)
	secret, err := term.ReadPassword(descriptor)
	_, _ = fmt.Fprintln(printer.Stdout)
	if err != nil {
		return "", fmt.Errorf("commands: reading the token: %w", err)
	}
	return strings.TrimSpace(string(secret)), nil
}

func displayOr(name, fallback string) string {
	if name != "" {
		return name
	}
	return fallback
}
