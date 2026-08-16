package cli

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cephalopod-ai/tagteam/internal/tagteam"
)

func newRouteCommand(shared *flagState) *cobra.Command {
	var list bool
	cmd := &cobra.Command{
		Use:   "route",
		Short: "Explain how a job would be routed to a workflow and a team, without running it",
		Long: `route resolves --job through the capability registry and prints the decision the
same way a run would compute it: the workflow the job selects, the agent chosen
for each slot, why it was chosen, the ranked fallbacks, and every candidate that
was rejected.

Nothing is executed and no run directory is created. Add --route-probe to detect
adapter availability first; without it routing assumes every configured adapter
is available and records that assumption in the decision.`,
		Example: `tagteam route --list
tagteam route --job audit
tagteam route --job scoped_patch --route-exclude grok --json`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if list {
				return renderRouteCatalog(cmd, shared)
			}
			if strings.TrimSpace(shared.Job) == "" {
				return &tagteam.ExitError{Code: tagteam.ExitInvalidArguments, Err: fmt.Errorf("route requires --job <name> or --list")}
			}
			opts, _, err := resolve(cmd, shared, strings.Join(args, " "))
			if err != nil {
				return err
			}
			if opts.Routing == nil {
				return &tagteam.ExitError{Code: tagteam.ExitInvalidArguments, Err: fmt.Errorf("no routing decision was produced for job %q", shared.Job)}
			}
			if shared.JSON {
				payload, err := json.MarshalIndent(opts.Routing, "", "  ")
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), string(payload))
				return nil
			}
			fmt.Fprint(cmd.OutOrStdout(), tagteam.FormatRoutingDecision(*opts.Routing))
			return nil
		},
	}
	cmd.Flags().BoolVar(&list, "list", false, "List the configured jobs and agent cards instead of routing one job")
	return cmd
}

type routeCatalog struct {
	Jobs   []routeCatalogJob   `json:"jobs"`
	Agents []routeCatalogAgent `json:"agents"`
}

type routeCatalogJob struct {
	Name            string   `json:"name"`
	Description     string   `json:"description,omitempty"`
	Workflow        string   `json:"workflow"`
	Rounds          int      `json:"rounds,omitempty"`
	DiverseReviewer bool     `json:"diverse_reviewer,omitempty"`
	Slots           []string `json:"slots"`
}

type routeCatalogAgent struct {
	Key           string         `json:"key"`
	Target        string         `json:"target"`
	Family        string         `json:"family"`
	Slots         []string       `json:"slots,omitempty"`
	ContextTokens int            `json:"context_tokens,omitempty"`
	Capabilities  map[string]int `json:"capabilities,omitempty"`
	Disabled      bool           `json:"disabled,omitempty"`
	Notes         string         `json:"notes,omitempty"`
}

func renderRouteCatalog(cmd *cobra.Command, flags *flagState) error {
	workdir, err := filepath.Abs(flags.Workdir)
	if err != nil {
		return err
	}
	changed := collectChangedFlags(cmd)
	cfg, _, err := tagteam.LoadConfigWithOptions(workdir, tagteam.LoadConfigOptions{
		TrustRepoConfig: flags.TrustRepoConfig && changed["trust-repo-config"],
	})
	if err != nil {
		return err
	}
	catalog, err := buildRouteCatalog(cfg)
	if err != nil {
		return err
	}
	if flags.JSON {
		payload, err := json.MarshalIndent(catalog, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), string(payload))
		return nil
	}
	out := cmd.OutOrStdout()
	fmt.Fprintln(out, "jobs:")
	for _, job := range catalog.Jobs {
		fmt.Fprintf(out, "  %-16s %-12s slots=%s\n", job.Name, job.Workflow, strings.Join(job.Slots, ","))
		if job.Description != "" {
			fmt.Fprintf(out, "  %-16s %s\n", "", job.Description)
		}
	}
	fmt.Fprintln(out, "\nagents:")
	for _, agent := range catalog.Agents {
		slots := "any"
		if len(agent.Slots) > 0 {
			slots = strings.Join(agent.Slots, ",")
		}
		state := ""
		if agent.Disabled {
			state = " (disabled)"
		}
		fmt.Fprintf(out, "  %-14s %-34s family=%-10s slots=%s%s\n", agent.Key, agent.Target, agent.Family, slots, state)
	}
	return nil
}

func buildRouteCatalog(cfg tagteam.Config) (routeCatalog, error) {
	catalog := routeCatalog{}
	for _, name := range tagteam.JobNames(cfg) {
		spec, err := tagteam.ResolveJob(cfg, name)
		if err != nil {
			return routeCatalog{}, err
		}
		slots := make([]string, 0, len(spec.Roles))
		for _, slot := range tagteam.SlotsForMode(spec.Mode) {
			slots = append(slots, string(slot))
		}
		catalog.Jobs = append(catalog.Jobs, routeCatalogJob{
			Name:            spec.Name,
			Description:     spec.Description,
			Workflow:        string(spec.Mode),
			Rounds:          spec.Rounds,
			DiverseReviewer: spec.DiverseReviewer,
			Slots:           slots,
		})
	}
	roster, err := tagteam.ResolveAgentRoster(cfg)
	if err != nil {
		return routeCatalog{}, err
	}
	for _, card := range roster {
		entry := routeCatalogAgent{
			Key:           card.Key,
			Target:        card.Target.Adapter,
			Family:        card.Family,
			ContextTokens: card.ContextTokens,
			Disabled:      card.Disabled,
			Notes:         card.Notes,
			Capabilities:  map[string]int{},
		}
		if card.Target.Model != "" {
			entry.Target = card.Target.Adapter + ":" + card.Target.Model
		}
		for _, slot := range card.Slots {
			entry.Slots = append(entry.Slots, string(slot))
		}
		for capability, level := range card.Capabilities {
			entry.Capabilities[string(capability)] = level
		}
		sort.Strings(entry.Slots)
		catalog.Agents = append(catalog.Agents, entry)
	}
	return catalog, nil
}
