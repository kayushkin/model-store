package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	ms "github.com/kayushkin/model-store"
	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:   "ms",
		Short: "Model store — centralized model registry, auth, and usage",
	}

	// providers
	root.AddCommand(&cobra.Command{
		Use:   "providers",
		Short: "List configured providers",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := ms.Open("")
			if err != nil {
				return err
			}
			defer store.Close()

			providers, err := store.Providers()
			if err != nil {
				return err
			}

			fmt.Printf("%-14s %-20s %-12s %s\n", "ID", "Name", "Auth", "Base URL")
			fmt.Println(strings.Repeat("─", 70))
			for _, p := range providers {
				fmt.Printf("%-14s %-20s %-12s %s\n", p.ID, p.Name, p.AuthType, p.BaseURL)
			}
			return nil
		},
	})

	// models
	root.AddCommand(&cobra.Command{
		Use:   "models [provider]",
		Short: "List available models",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := ms.Open("")
			if err != nil {
				return err
			}
			defer store.Close()

			providers, _ := store.Providers()
			for _, p := range providers {
				if len(args) > 0 && args[0] != p.ID {
					continue
				}
				models, _ := store.Models(p.ID)
				if len(models) == 0 {
					continue
				}
				fmt.Printf("\n%s:\n", p.Name)
				for _, m := range models {
					aliases := ""
					if len(m.Aliases) > 0 {
						aliases = " (" + strings.Join(m.Aliases, ", ") + ")"
					}
					fmt.Printf("  %-35s $%.2f/$%.2f per MTok%s\n", m.ID, m.InputCost, m.OutputCost, aliases)
				}
			}
			return nil
		},
	})

	// resolve
	root.AddCommand(&cobra.Command{
		Use:   "resolve <model>",
		Short: "Resolve a model and show its provider/credentials",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := ms.Open("")
			if err != nil {
				return err
			}
			defer store.Close()

			creds, model, err := store.ResolveForModel(args[0])
			if err != nil {
				return err
			}

			fmt.Printf("Model:    %s (%s)\n", model.ID, model.Name)
			fmt.Printf("Provider: %s\n", model.Provider)
			fmt.Printf("Context:  %d tokens\n", model.MaxTokens)
			fmt.Printf("Cost:     $%.2f in / $%.2f out per MTok\n", model.InputCost, model.OutputCost)
			if creds != nil {
				fmt.Printf("Auth:     %s\n", creds.AuthType)
				if creds.APIKey != "" {
					fmt.Printf("Key:      %s...%s\n", creds.APIKey[:8], creds.APIKey[len(creds.APIKey)-4:])
				}
				if creds.Token != "" {
					fmt.Printf("Token:    (set, expires %s)\n", creds.ExpiresAt.Format(time.RFC3339))
				}
			}
			return nil
		},
	})

	// seed
	root.AddCommand(&cobra.Command{
		Use:   "seed",
		Short: "Seed default providers and models",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := ms.Open("")
			if err != nil {
				return err
			}
			defer store.Close()

			if err := store.Seed(); err != nil {
				return err
			}
			fmt.Println("Seeded providers and models.")
			return nil
		},
	})

	// usage
	var usageAgent string
	var usageToday, usageWeek bool
	usageCmd := &cobra.Command{
		Use:   "usage",
		Short: "Show token usage",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := ms.Open("")
			if err != nil {
				return err
			}
			defer store.Close()

			date := ""
			if usageToday {
				date = time.Now().Format("2006-01-02")
			}

			if usageWeek {
				from := time.Now().AddDate(0, 0, -7).Format("2006-01-02")
				to := time.Now().Format("2006-01-02")
				records, err := store.UsageSummary(from, to)
				if err != nil {
					return err
				}
				fmt.Print(ms.PrintUsage(records))
				return nil
			}

			records, err := store.Usage(usageAgent, date)
			if err != nil {
				return err
			}
			fmt.Print(ms.PrintUsage(records))
			return nil
		},
	}
	usageCmd.Flags().StringVar(&usageAgent, "agent", "", "Filter by agent")
	usageCmd.Flags().BoolVar(&usageToday, "today", false, "Show today's usage")
	usageCmd.Flags().BoolVar(&usageWeek, "week", false, "Show this week's usage")
	root.AddCommand(usageCmd)

	// keys add
	keysCmd := &cobra.Command{Use: "keys", Short: "Manage API keys"}
	keysAddCmd := &cobra.Command{
		Use:   "add <provider> <key>",
		Short: "Add an API key for a provider",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := ms.Open("")
			if err != nil {
				return err
			}
			defer store.Close()

			err = store.SetCredentials(ms.Credentials{
				Provider: args[0],
				AuthType: "api_key",
				APIKey:   args[1],
			})
			if err != nil {
				return err
			}
			fmt.Printf("API key set for %s.\n", args[0])
			return nil
		},
	}
	keysCmd.AddCommand(keysAddCmd)
	root.AddCommand(keysCmd)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
