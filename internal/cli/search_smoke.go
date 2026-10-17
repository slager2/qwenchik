package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/slager2/qwenchik/internal/config"
	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/spf13/cobra"
)

func newSearchSmokeCommand(jsonMode *bool) *cobra.Command {
	command := &cobra.Command{
		Use:   "search-smoke",
		Short: "Run one bounded search probe; only local SearXNG unless external calls are explicitly allowed",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			allowExternal, _ := cmd.Flags().GetBool("allow-external")
			query, _ := cmd.Flags().GetString("query")
			var provider research.SearchProvider
			if allowExternal {
				fmt.Fprintln(cmd.ErrOrStderr(), "External search is enabled for this probe; Tavily may charge for the request.")
				provider, err = configuredSearchProvider(cfg)
			} else if cfg.SearXNGURL != "" {
				provider, err = research.NewSearXNG(cfg.SearXNGURL, nil)
			} else {
				return errors.New("no local SearXNG configured; use --allow-external to permit external search providers")
			}
			if err != nil {
				return err
			}
			diagnostic, ok := provider.(research.DiagnosticSearchProvider)
			if !ok {
				return errors.New("no diagnostic search provider is enabled")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			report, searchErr := diagnostic.SearchWithDiagnostics(ctx, research.SearchOptions{Query: query, MaxResults: 3, SafeSearch: true})
			printValue(output{json: *jsonMode, out: cmd.OutOrStdout()}, map[string]any{"usable_hits": len(report.Hits), "outcomes": report.Outcomes})
			return searchErr
		},
	}
	command.Flags().Bool("allow-external", false, "allow DuckDuckGo and optional billable Tavily requests")
	command.Flags().String("query", "библиотеки в Астане", "bounded probe query")
	return command
}
