package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/stone-age-io/stone-cli/internal/ctx"
	"github.com/stone-age-io/stone-cli/internal/pb"
	"golang.org/x/term"
)

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage PocketBase authentication for the active context",
}

var authLoginCmd = &cobra.Command{
	Use:   "login",
	Short: "Log in to the active context's PocketBase server",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx.Active(flagContext)
		if err != nil {
			return err
		}
		collection, _ := cmd.Flags().GetString("collection")
		email, _ := cmd.Flags().GetString("email")
		password, _ := cmd.Flags().GetString("password")

		if collection == "" {
			if c.Auth.Collection != "" {
				collection = c.Auth.Collection
			} else {
				collection = "users"
			}
		}
		if email == "" {
			if c.Auth.Email != "" {
				fmt.Printf("Email [%s]: ", c.Auth.Email)
			} else {
				fmt.Print("Email: ")
			}
			r := bufio.NewReader(os.Stdin)
			line, _ := r.ReadString('\n')
			email = strings.TrimSpace(line)
			if email == "" {
				email = c.Auth.Email
			}
		}
		if email == "" {
			return errors.New("email is required")
		}
		if password == "" {
			fmt.Print("Password: ")
			pw, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Println()
			if err != nil {
				return fmt.Errorf("read password: %w", err)
			}
			password = string(pw)
		}
		if password == "" {
			return errors.New("password is required")
		}

		client := pb.New(c)
		if err := client.Health(); err != nil {
			return fmt.Errorf("cannot reach PocketBase at %s: %w", c.URL, err)
		}
		ar, err := client.AuthWithPassword(collection, email, password)
		if err != nil {
			return err
		}

		c.Auth.Collection = collection
		c.Auth.Token = ar.Token
		c.Auth.Email = email
		if id, ok := ar.Record["id"].(string); ok {
			c.Auth.UserID = id
		}
		// Best-effort: capture current_organization so it's pre-loaded.
		if cur, ok := ar.Record["current_organization"].(string); ok && cur != "" {
			c.CurrentOrganization = cur
		}
		if err := ctx.Save(c); err != nil {
			return err
		}
		fmt.Printf("logged in as %s (collection=%s)\n", email, collection)
		if c.CurrentOrganization != "" {
			fmt.Printf("current_organization: %s\n", c.CurrentOrganization)
		}
		return nil
	},
}

var authLogoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Clear the auth token from the active context",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx.Active(flagContext)
		if err != nil {
			return err
		}
		c.Auth.Token = ""
		c.Auth.Expires = ""
		if err := ctx.Save(c); err != nil {
			return err
		}
		fmt.Printf("logged out of context %q\n", c.Name)
		return nil
	},
}

var authWhoamiCmd = &cobra.Command{
	Use:   "whoami",
	Short: "Print the current authenticated user",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx.Active(flagContext)
		if err != nil {
			return err
		}
		if c.Auth.Token == "" {
			return errors.New("not logged in. run: stone auth login")
		}
		fmt.Printf("context:              %s\n", c.Name)
		fmt.Printf("url:                  %s\n", c.URL)
		fmt.Printf("collection:           %s\n", c.Auth.Collection)
		fmt.Printf("email:                %s\n", c.Auth.Email)
		fmt.Printf("user_id:              %s\n", c.Auth.UserID)
		fmt.Printf("current_organization: %s\n", or(c.CurrentOrganization, "(unset)"))
		return nil
	},
}

func init() {
	authLoginCmd.Flags().String("collection", "", "auth collection (default: users)")
	authLoginCmd.Flags().String("email", "", "email/identity (prompted if not set)")
	authLoginCmd.Flags().String("password", "", "password (prompted if not set)")

	authCmd.AddCommand(authLoginCmd, authLogoutCmd, authWhoamiCmd)
	rootCmd.AddCommand(authCmd)
}
