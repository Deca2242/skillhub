package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
)

// The skills.sh catalog (S14): skills search and skills popular. Both only
// read; installing a result is skills install, with its review and plan.

// defaultPopularLimit is how many popular skills are shown by default.
const defaultPopularLimit = 30

// catalogFlags are the flags search and popular share.
type catalogFlags struct {
	limit   int
	asJSON  bool
	refresh bool
}

func (cf *catalogFlags) add(c *cobra.Command, defaultLimit int) {
	c.Flags().IntVar(&cf.limit, "limit", defaultLimit, text.FlagCatalogLimit)
	c.Flags().BoolVar(&cf.asJSON, "json", false, flagJSON)
	c.Flags().BoolVar(&cf.refresh, "refresh", false, text.FlagRefresh)
}

func (cf *catalogFlags) check() error {
	if cf.limit < 1 || cf.limit > 100 {
		return fail(text.CatalogLimitRange, cf.limit)
	}
	return nil
}

func (r *runner) searchCmd() *cobra.Command {
	var cf catalogFlags
	c := &cobra.Command{
		Use: text.SearchUse, Short: text.SearchShort, Long: text.SearchLong,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) < 1 {
				return fail(msgArgsMin, cmd.CommandPath(), 1)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cf.check(); err != nil {
				return err
			}
			a, err := r.facade(false)
			if err != nil {
				return err
			}
			query := strings.Join(args, " ")
			res, err := a.SearchCatalog(r.ctx, query, cf.limit, app.CatalogOptions{Refresh: cf.refresh})
			if err != nil {
				return err
			}
			return r.printCatalog(cmd, cf, res, fmt.Sprintf(text.CatalogNoResults, text.SafeLine(strings.TrimSpace(query))))
		},
	}
	cf.add(c, app.DefaultCatalogLimit)
	return c
}

func (r *runner) popularCmd() *cobra.Command {
	var cf catalogFlags
	c := &cobra.Command{
		Use: text.PopularUse, Short: text.PopularShort, Long: text.PopularLong, Args: exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cf.check(); err != nil {
				return err
			}
			a, err := r.facade(false)
			if err != nil {
				return err
			}
			res, err := a.PopularCatalog(r.ctx, app.CatalogOptions{Refresh: cf.refresh})
			if err != nil {
				return err
			}
			if len(res.Entries) > cf.limit {
				res.Entries = res.Entries[:cf.limit]
			}
			return r.printCatalog(cmd, cf, res, text.CatalogNoPopular)
		},
	}
	cf.add(c, defaultPopularLimit)
	return c
}

// printCatalog writes the warnings to stderr, then the entries as JSON or
// as a table followed by how to install one.
func (r *runner) printCatalog(cmd *cobra.Command, cf catalogFlags, res app.CatalogResult, empty string) error {
	errOut := cmd.ErrOrStderr()
	if res.Stale != nil {
		fmt.Fprint(errOut, text.CatalogStale(res.Stale, res.CachedAt))
	}
	if res.CacheErr != nil {
		fmt.Fprintf(errOut, text.CatalogCacheNotSaved, text.SafeLine(res.CacheErr.Error()))
	}
	out := cmd.OutOrStdout()
	if cf.asJSON {
		entries := res.Entries
		if entries == nil {
			entries = []app.CatalogEntry{}
		}
		return writeJSON(out, entries)
	}
	if len(res.Entries) == 0 {
		fmt.Fprint(out, empty)
		return nil
	}
	fmt.Fprint(out, text.CatalogTable(res.Entries)+"\n"+text.CatalogInstallHint(res.Entries))
	return nil
}
