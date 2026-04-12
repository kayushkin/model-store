package main

import (
	"fmt"
	"os"
	"strings"

	ms "github.com/kayushkin/model-store"
	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:   "ms",
		Short: "Model store — centralized model registry",
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

			fmt.Printf("%-14s %-20s\n", "ID", "Name")
			fmt.Println(strings.Repeat("─", 36))
			for _, p := range providers {
				fmt.Printf("%-14s %-20s\n", p.ID, p.Name)
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
					status := ""
					if !m.Enabled {
						status = " [disabled]"
					}
					fmt.Printf("  %-35s $%.2f/$%.2f per MTok%s%s\n", m.ID, m.InputCost, m.OutputCost, aliases, status)
				}
			}
			return nil
		},
	})

	// resolve
	root.AddCommand(&cobra.Command{
		Use:   "resolve <model>",
		Short: "Resolve a model and show its details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := ms.Open("")
			if err != nil {
				return err
			}
			defer store.Close()

			model, err := store.ResolveModel(args[0])
			if err != nil {
				return err
			}

			fmt.Printf("Model:    %s (%s)\n", model.ID, model.Name)
			fmt.Printf("Provider: %s\n", model.Provider)
			fmt.Printf("Context:  %d tokens\n", model.MaxTokens)
			fmt.Printf("Cost:     $%.2f in / $%.2f out per MTok\n", model.InputCost, model.OutputCost)
			fmt.Printf("Enabled:  %v\n", model.Enabled)
			fmt.Printf("Priority: %d\n", model.Priority)
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

	// enable/disable
	root.AddCommand(&cobra.Command{
		Use:   "enable <model>",
		Short: "Enable a model",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := ms.Open("")
			if err != nil {
				return err
			}
			defer store.Close()
			if err := store.SetEnabled(args[0], true); err != nil {
				return err
			}
			fmt.Printf("Enabled %s\n", args[0])
			return nil
		},
	})

	root.AddCommand(&cobra.Command{
		Use:   "disable <model>",
		Short: "Disable a model",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := ms.Open("")
			if err != nil {
				return err
			}
			defer store.Close()
			if err := store.SetEnabled(args[0], false); err != nil {
				return err
			}
			fmt.Printf("Disabled %s\n", args[0])
			return nil
		},
	})

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
