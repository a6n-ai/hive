package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/a6n-ai/inscribe"
	"github.com/a6n-ai/inscribe/internal/recall"
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
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(stdout, "labeled=%d hits=%d misses=%d quiet=%d\n", res.Labeled, res.Hits, res.Misses, res.Quiet); err != nil {
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
