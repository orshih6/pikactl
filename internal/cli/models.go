package cli

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/orshih6/pikactl/internal/catalog"
	"github.com/orshih6/pikactl/internal/output"
)

func newModelsCommand() *cobra.Command {
	var category, vendor string
	var refresh bool
	cmd := &cobra.Command{
		Use:     "models [filter]",
		Aliases: []string{"model"},
		Short:   "List the operations in Pika's live catalog",
		Long: "List the operations in Pika's live catalog, with their starting prices. It is\n" +
			"public, so this needs no key. The list is kept for an hour; --refresh gets it\n" +
			"again. The filter matches any part of an ID or name.",
		Example: `  pikactl models
  pikactl models --category video --vendor pika
  pikactl models seedance`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			ops, err := a.catalog.List(cmd.Context(), refresh)
			if err != nil {
				return err
			}
			filter := ""
			if len(args) == 1 {
				filter = strings.ToLower(args[0])
			}
			rows := [][]string{}
			matched := []catalog.Op{}
			for _, op := range ops {
				if category != "" && !strings.EqualFold(op.Category, category) {
					continue
				}
				if vendor != "" && !strings.EqualFold(op.Vendor, vendor) {
					continue
				}
				if filter != "" && !strings.Contains(strings.ToLower(op.ID+" "+op.Name), filter) {
					continue
				}
				matched = append(matched, op)
				rows = append(rows, []string{op.ID, op.Category, op.PriceFrom(), op.Name})
			}
			return emit(a.out, matched, func() error {
				if err := output.Table(a.out, []string{"ID", "CATEGORY", "FROM", "NAME"}, rows); err != nil {
					return err
				}
				a.logf("%d of %d operations", len(rows), len(ops))
				if len(rows) == 0 && category != "" {
					a.logf("categories: %s", strings.Join(categories(ops), ", "))
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&category, "category", "", "only this category: video, image, audio, llm or decision")
	cmd.Flags().StringVar(&vendor, "vendor", "", "only this vendor, such as pika or google")
	cmd.Flags().BoolVar(&refresh, "refresh", false, "ask the API instead of using the saved list")
	cmd.AddCommand(
		newModelsShowCommand(),
		newModelsDocsCommand(),
		newModelsCompetitorsCommand(),
	)
	return cmd
}

// categories lists the categories present in the catalog, in order.
func categories(ops []catalog.Op) []string {
	seen := map[string]bool{}
	var out []string
	for _, op := range ops {
		if op.Category != "" && !seen[op.Category] {
			seen[op.Category] = true
			out = append(out, op.Category)
		}
	}
	sort.Strings(out)
	return out
}

func newModelsCompetitorsCommand() *cobra.Command {
	var refresh bool
	cmd := &cobra.Command{
		Use:   "competitors [filter]",
		Short: "Compare Pika's prices with what others charge for the same operation",
		Long: "Compare Pika's price for an operation and setting with the other providers'\n" +
			"prices that Pika publishes. It is public, so this needs no key. The filter\n" +
			"matches any part of an operation ID; --json prints every quote in each row.",
		Example: `  pikactl models competitors
  pikactl models competitors seedance`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			comp, err := a.catalog.Competitors(cmd.Context(), refresh)
			if err != nil {
				return err
			}
			filter := ""
			if len(args) == 1 {
				filter = strings.ToLower(args[0])
			}
			matched := comp.Rows[:0:0]
			for _, row := range comp.Rows {
				if filter == "" || strings.Contains(strings.ToLower(row.APIID), filter) {
					matched = append(matched, row)
				}
			}
			total := len(comp.Rows)
			comp.Rows = matched
			return emit(a.out, comp, func() error {
				// Pika's own prices come from the catalog; without it the
				// other providers' prices are still worth showing.
				byID := map[string]*catalog.Op{}
				if ops, err := a.catalog.List(cmd.Context(), refresh); err == nil {
					for i := range ops {
						byID[ops[i].ID] = &ops[i]
					}
				}
				names := map[string]string{}
				for _, c := range comp.Competitors {
					names[c.ID] = c.Name
				}
				rows := make([][]string, 0, len(matched))
				for _, row := range matched {
					pika := ""
					if op := byID[row.APIID]; op != nil {
						pika = op.PriceAt(row.Spec)
					}
					rows = append(rows, []string{row.APIID, row.SpecLabel(), pika, cheapest(row, names), strconv.Itoa(len(row.Quotes))})
				}
				if err := output.Table(a.out, []string{"ID", "SETTING", "PIKA", "CHEAPEST ELSEWHERE", "OTHERS"}, rows); err != nil {
					return err
				}
				a.logf("%d of %d rows, recorded %s", len(matched), total, comp.RecordedAt)
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&refresh, "refresh", false, "ask the API instead of using the saved list")
	return cmd
}

// cheapest names the lowest of the other providers' prices in a row.
func cheapest(row catalog.CompetitorRow, names map[string]string) string {
	best, bestUSD := -1, 0.0
	for i, q := range row.Quotes {
		usd, err := strconv.ParseFloat(q.USD, 64)
		if err != nil {
			continue
		}
		if best < 0 || usd < bestUSD {
			best, bestUSD = i, usd
		}
	}
	if best < 0 {
		return ""
	}
	q := row.Quotes[best]
	name := names[q.Competitor]
	if name == "" {
		name = q.Competitor
	}
	return fmt.Sprintf("%s $%s/%s", name, q.USD, q.Unit)
}

func newModelsShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Show an operation's prices and parameters",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			op, err := a.catalog.Get(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return emit(a.out, op, func() error {
				how := "`pikactl chat`, see `pikactl models docs " + op.ID + "`"
				if op.Call != nil {
					how = op.Call.Method + " " + op.Call.Path
				} else if op.Category == "decision" {
					how = "`pikactl decide`"
				}
				if err := output.Fields(a.out, []output.Field{
					{Name: "id", Value: op.ID},
					{Name: "name", Value: op.Name},
					{Name: "category", Value: op.Category},
					{Name: "called with", Value: how},
					{Name: "description", Value: op.Description},
					{Name: "prices", Value: strings.Join(op.PriceLines(), "\n")},
				}); err != nil {
					return err
				}
				schema, err := catalog.ParseSchema(op.InputSchema)
				if err != nil {
					return err
				}
				params := schema.Params()
				if len(params) == 0 {
					return nil
				}
				fmt.Fprintln(a.out, "parameters:")
				for _, p := range params {
					fmt.Fprintf(a.out, "  --%s (%s)\n      %s\n", strings.ReplaceAll(p.Name, "_", "-"), p.TypeName(), p.Usage())
				}
				return nil
			})
		},
	}
}

func newModelsDocsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "docs <id>",
		Short: "Print an operation's full specification from Pika's docs",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			spec, err := a.catalog.Docs(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			fmt.Fprint(a.out, spec)
			return nil
		},
	}
}
