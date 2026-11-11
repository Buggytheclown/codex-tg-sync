package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/mideco-tech/codex-tg/internal/config"
	"github.com/mideco-tech/codex-tg/internal/storage"
)

func runBoard(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "migrate-card-types" {
		return errors.New("usage: ctr-go board migrate-card-types --dry-run [--output file] | --apply report")
	}
	fs := flag.NewFlagSet("board migrate-card-types", flag.ContinueOnError)
	fs.SetOutput(out)
	dry := fs.Bool("dry-run", false, "read-only migration preview")
	output := fs.String("output", "", "private preview JSON destination")
	apply := fs.String("apply", "", "apply an unchanged preview with the daemon stopped")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 || (*dry == (*apply != "")) || (!*dry && *output != "") {
		return errors.New("choose exactly one of --dry-run or --apply report")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if *dry {
		report, err := storage.PreviewCardTypeMigration(ctx, cfg.Paths.DBPath)
		if err != nil {
			return err
		}
		destination := out
		if *output != "" {
			file, err := os.OpenFile(*output, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			defer file.Close()
			if err = file.Chmod(0o600); err != nil {
				return err
			}
			destination = file
		}
		encoder := json.NewEncoder(destination)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	if cfg.ControlAPIListen != "" {
		conn, err := net.DialTimeout("tcp", cfg.ControlAPIListen, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return errors.New("stop the daemon before applying the migration")
		}
	}
	file, err := os.Open(*apply)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 8<<20))
	decoder.DisallowUnknownFields()
	var report storage.CardTypeMigration
	if err = decoder.Decode(&report); err != nil {
		return err
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return errors.New("migration report must contain one JSON object")
	}
	if err = storage.ApplyCardTypeMigration(ctx, cfg.Paths.DBPath, report); err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, "Card types migrated; legacy Task PR references archived; parent links remain manual.")
	return err
}
