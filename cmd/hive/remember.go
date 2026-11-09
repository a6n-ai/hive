package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/a6n-ai/hive"
	"github.com/a6n-ai/hive/model"
)

var (
	memoryTypes = map[string]model.MemoryType{"note": model.Note, "preference": model.Preference, "decision": model.Decision}
	scopeKinds  = map[string]model.ScopeKind{"global": model.Global, "project": model.ProjectScope}
)

func rememberCmd(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("remember", flag.ContinueOnError)
	typ := fs.String("type", "note", "note, preference, or decision")
	scope := fs.String("scope", "project", "global or project")
	project := fs.String("project", defaultProject(), "project path, used for project scope")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: hive remember TEXT [--type T] [--scope S] [--project P]")
	}
	t, ok := memoryTypes[*typ]
	if !ok {
		return fmt.Errorf("unknown type %q", *typ)
	}
	s, ok := scopeKinds[*scope]
	if !ok {
		return fmt.Errorf("unknown scope %q", *scope)
	}
	h, err := hive.Open(hive.Options{Caller: "hive", Project: *project})
	if err != nil {
		return err
	}
	defer func() { _ = h.Close() }()
	id, err := h.Remember(context.Background(), pos[0], t, s, *project)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, id)
	return err
}
