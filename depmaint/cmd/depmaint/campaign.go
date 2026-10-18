package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/Fewbytes/sweatshop/depmaint/internal/campaign"
	"github.com/Fewbytes/sweatshop/depmaint/internal/cli"
)

const campaignUsage = `usage: depmaint campaign <subcommand>

  new --type upgrade|runtime --target ecosystem:name=version [--budget N]
  plan <id>
  next <id>
  record <id> --step N --verdict succeeded|rejected|failed [--reason R] [--pr URL] [--executor NAME]
  list [--status S]`

// runCampaign dispatches `depmaint campaign ...`; exit codes as run.
func runCampaign(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, campaignUsage)
		return 2
	}
	ctx, pub := context.Background(), cli.DefaultPublished()
	fs := flag.NewFlagSet("campaign "+args[0], flag.ContinueOnError)
	var err error
	switch args[0] {
	case "new":
		typ := fs.String("type", "upgrade", "upgrade or runtime")
		target := fs.String("target", "", "ecosystem:name=version")
		budget := fs.Int("budget", 0, "attempt limit (default 5)")
		if fs.Parse(args[1:]) != nil {
			return 2
		}
		err = cli.CampaignNew(ctx, ".", cli.CampaignNewArgs{Type: campaign.Type(*typ), Target: *target, Attempts: *budget}, pub, stdout)
	case "plan", "next":
		if len(args) != 2 {
			fmt.Fprintln(stderr, campaignUsage)
			return 2
		}
		if args[0] == "plan" {
			err = cli.CampaignPlan(ctx, ".", args[1], pub, stdout)
		} else {
			err = cli.CampaignNext(ctx, ".", args[1], pub, stdout)
		}
	case "record":
		if len(args) < 2 {
			fmt.Fprintln(stderr, campaignUsage)
			return 2
		}
		step := fs.Int("step", 0, "step number from the work order")
		verdict := fs.String("verdict", "", "succeeded, rejected or failed")
		reason := fs.String("reason", "", "required for rejected")
		pr := fs.String("pr", "", "pull request URL")
		commit := fs.String("commit", "", "commit sha")
		executor := fs.String("executor", "", "who executed the work order")
		if fs.Parse(args[2:]) != nil {
			return 2
		}
		err = cli.CampaignRecord(ctx, ".", campaign.RecordInput{CampaignID: args[1], Step: *step, Verdict: campaign.Verdict(*verdict),
			Reason: *reason, Executor: *executor, PRURL: *pr, CommitSHA: *commit}, pub, stdout)
	case "list":
		status := fs.String("status", "", "only campaigns with this status")
		if fs.Parse(args[1:]) != nil {
			return 2
		}
		err = cli.CampaignList(ctx, ".", campaign.Status(*status), stdout)
	default:
		fmt.Fprintln(stderr, campaignUsage)
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "campaign %s: %v\n", args[0], err)
		return 3
	}
	return 0
}
