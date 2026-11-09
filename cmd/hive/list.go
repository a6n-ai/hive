package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/a6n-ai/hive"
	"github.com/a6n-ai/hive/model"
)

func listCmd(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	typ := fs.String("type", "", "memory type")
	project := fs.String("project", "", "project path")
	status := fs.String("status", "", "memory status")
	if err := fs.Parse(args); err != nil {
		return err
	}
	f := hive.ListFilter{Project: *project}
	if *typ != "" {
		f.Types = []model.MemoryType{model.MemoryType(*typ)}
	}
	if *status != "" {
		f.Statuses = []model.Status{model.Status(*status)}
	}
	h, err := hive.Open(hive.Options{Caller: "hive", Project: *project})
	if err != nil {
		return err
	}
	defer func() { _ = h.Close() }()
	ms, err := h.List(context.Background(), f)
	if err != nil {
		return err
	}
	for _, m := range ms {
		fmt.Fprintf(stdout, "%s [%s] %s\n", m.ID, m.Type, m.Text)
	}
	return nil
}
