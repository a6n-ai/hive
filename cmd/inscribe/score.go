package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/a6n-ai/inscribe"
	"github.com/a6n-ai/inscribe/internal/config"
	"github.com/a6n-ai/inscribe/internal/recall"
	"github.com/a6n-ai/inscribe/internal/store"
)

const scoreUsage = "usage: inscribe score (train|show [--outcomes]|combine --dir DIR)"

func scoreCmd(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New(scoreUsage)
	}
	if args[0] == "combine" {
		return combineCmd(args[1:], stdout)
	}
	home := config.Home()
	h, err := inscribe.Open(inscribe.Options{Caller: "inscribe"})
	if err != nil {
		return err
	}
	defer func() { _ = h.Close() }()
	switch args[0] {
	case "train":
		res, err := h.TrainScores(context.Background(), time.Now())
		if errors.Is(err, store.ErrLocked) {
			_, err := fmt.Fprintln(stdout, "skipped: another writer holds the store")
			return err
		}
		if err != nil {
			return err
		}
		reasons := make([]string, 0, len(res.Reasons))
		for r := range res.Reasons {
			reasons = append(reasons, r)
		}
		sort.Strings(reasons)
		for _, r := range reasons {
			if _, err := fmt.Fprintf(stdout, "%s=%d\n", r, res.Reasons[r]); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(stdout, "open=%d\n", res.Open); err != nil {
			return err
		}
		if err := writeWeights(stdout, res.Weights); err != nil {
			return err
		}
		shareSnapshot(h, home, res.Weights)
		return nil
	case "show":
		fs := flag.NewFlagSet("show", flag.ContinueOnError)
		outcomes := fs.Bool("outcomes", false, "list the latest outcome groups")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		w, err := h.ScoreWeights()
		if err != nil {
			return err
		}
		if err := writeWeights(stdout, w); err != nil {
			return err
		}
		if !*outcomes {
			return nil
		}
		return writeOutcomes(stdout, h)
	default:
		return errors.New(scoreUsage)
	}
}

func writeWeights(w io.Writer, weights recall.Weights) error {
	_, err := fmt.Fprintf(w, "text=%.3f key=%.3f body=%.3f type=%.3f scope=%.3f agent=%.3f command=%.3f trajectory=%.3f seen=%d hits=%d misses=%d\n",
		weights.Text, weights.Key, weights.Body, weights.Type, weights.Scope, weights.Agent, weights.Command, weights.Trajectory,
		weights.Seen, weights.Hits, weights.Misses)
	return err
}

// writeOutcomes lists the latest 20 groups: each digest prefix, then one line per row.
func writeOutcomes(w io.Writer, h *inscribe.Inscribe) error {
	groups, err := h.RecentScoreOutcomes(20)
	if err != nil {
		return err
	}
	for _, g := range groups {
		digest := g[0].PromptDigest
		if len(digest) > 8 {
			digest = digest[:8]
		}
		if _, err := fmt.Fprintln(w, digest); err != nil {
			return err
		}
		for _, o := range g {
			label := "-"
			if o.Label != nil {
				label = strconv.Itoa(*o.Label)
			}
			if _, err := fmt.Fprintf(w, "%s %s %.3f %s\n", o.Reason, label, o.Scale, o.ShownKey); err != nil {
				return err
			}
		}
	}
	return nil
}

func combineCmd(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("combine", flag.ContinueOnError)
	dir := fs.String("dir", "", "directory of snapshot *.json files")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" {
		return errors.New("usage: inscribe score combine --dir DIR")
	}
	files, err := filepath.Glob(filepath.Join(*dir, "*.json"))
	if err != nil {
		return err
	}
	var snaps []Snapshot
	var mtimes []time.Time
	for _, f := range files {
		info, err := os.Stat(f)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(f) //nolint:gosec // G304: the maintainer names the directory
		if err != nil {
			return err
		}
		var s Snapshot
		if err := json.Unmarshal(raw, &s); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "skipped %s: %v\n", f, err)
			continue
		}
		snaps = append(snaps, s)
		mtimes = append(mtimes, info.ModTime())
	}
	return writeWeights(stdout, Combine(recall.DefaultWeights(), snaps, mtimes))
}
