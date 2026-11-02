package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/brendatamaar/arcana/internal/config"
	"github.com/brendatamaar/arcana/internal/doctor"
)

func newDoctorCommand() *cobra.Command {
	return &cobra.Command{Use: "doctor", Short: "Diagnose host prerequisites and runtime cleanup", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		filename, err := cmd.Flags().GetString("config")
		if err != nil {
			return err
		}
		cfg, err := config.Load(filename)
		if err != nil {
			return err
		}
		report := doctor.Run(cmd.Context(), cfg, doctor.Live{Config: cfg})
		if jsonMode(cmd) {
			if err = json.NewEncoder(cmd.OutOrStdout()).Encode(report); err != nil {
				return err
			}
		} else {
			writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			for _, check := range report.Checks {
				if _, err = fmt.Fprintf(writer, "%s\t%s\t%s\n", check.Status, check.Name, check.Detail); err != nil {
					return err
				}
				if check.Hint != "" {
					if _, err = fmt.Fprintf(writer, "\tfix\t%s\n", check.Hint); err != nil {
						return err
					}
				}
			}
			if err = writer.Flush(); err != nil {
				return err
			}
		}
		return requireHealthy(report)
	}}
}

func requireHealthy(report doctor.Report) error {
	if report.Failed() {
		return errors.New("host prerequisites failed; run arcana doctor for fix hints")
	}
	return nil
}

func startupDoctor(ctx context.Context, cfg config.Config) doctor.Report {
	return doctor.Run(ctx, cfg, doctor.Live{Config: cfg})
}
