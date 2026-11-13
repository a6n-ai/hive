package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/a6n-ai/inscribe"
	"github.com/a6n-ai/inscribe/internal/recall"
	"github.com/a6n-ai/inscribe/internal/store"
)

const scoreUsage = "usage: inscribe score (train|show)"

func scoreCmd(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New(scoreUsage)
	}
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
		return writeWeights(stdout, res.Weights)
	case "show":
		w, err := h.ScoreWeights()
		if err != nil {
			return err
		}
		return writeWeights(stdout, w)
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
