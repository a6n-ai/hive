package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/a6n-ai/inscribe"
)

func whyCmd(args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: inscribe why ID")
	}
	h, err := inscribe.Open(inscribe.Options{Caller: "inscribe"})
	if err != nil {
		return err
	}
	defer func() { _ = h.Close() }()
	evs, err := h.Why(context.Background(), args[0])
	if err != nil {
		return err
	}
	for _, e := range evs {
		text := e.PromptText
		if text == "" {
			b, _ := json.Marshal(e.Payload)
			text = string(b)
		}
		fmt.Fprintf(stdout, "%d [%s] %s\n", e.ID, e.Kind, text)
	}
	return nil
}
