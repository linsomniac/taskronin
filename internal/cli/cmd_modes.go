package cli

import "github.com/spf13/cobra"

func init() {
	for _, verb := range []string{"tui", "plan", "recap"} {
		register(func(_ *env) *cobra.Command { return modeCommand(verb) })
	}
}

func modeCommand(verb string) *cobra.Command {
	req := TUIRequest{Mode: verb}
	cmd := &cobra.Command{
		Use: verb, Short: "Open the TUI in " + verb + " mode", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return dispatchTUI(cmd, req) },
	}
	switch verb {
	case "tui":
		req.Mode = "tabs"
		cmd.Flags().StringVar(&req.Tab, "tab", "", "Initial tab: today, triage, backlog, upcoming or log")
	case "recap":
		cmd.Flags().StringVar(&req.RecapDate, "date", "", "Date to recap (default today)")
	}
	return cmd
}

func dispatchTUI(cmd *cobra.Command, req TUIRequest) error {
	if launchTUI == nil {
		return usagef("the TUI is not built in")
	}
	// Plan C validates these raw strings and resolves dates against its clock.
	return launchTUI(cmd, req)
}
