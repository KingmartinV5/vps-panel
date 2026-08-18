package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"

	"vps-panel/internal/store"
)

// readSecret reads one line without echo when stdin is a real terminal.
// When it isn't (piped input, e.g. scripted provisioning), it falls back to
// a plain stdin read -- same graceful degradation as Python's getpass.getpass().
func readSecret(stdin *bufio.Reader, prompt string) (string, error) {
	fmt.Print(prompt)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		pw, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		return string(pw), err
	}
	fmt.Fprintln(os.Stderr, "Warning: password input may be echoed (stdin is not a terminal).")
	line, err := stdin.ReadString('\n')
	return strings.TrimRight(line, "\r\n"), err
}

func promptPassword() string {
	stdin := bufio.NewReader(os.Stdin)
	for {
		pw, err := readSecret(stdin, "Password: ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "error reading password: %v\n", err)
			os.Exit(1)
		}
		if len(pw) < 8 {
			fmt.Println("Password must be at least 8 characters.")
			continue
		}
		confirm, err := readSecret(stdin, "Confirm password: ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "error reading password: %v\n", err)
			os.Exit(1)
		}
		if string(pw) != string(confirm) {
			fmt.Println("Passwords did not match.")
			continue
		}
		return string(pw)
	}
}

func manageCreateUser(st *store.Store, username string, isAdmin bool) {
	if existing, _ := st.GetUserByUsername(username); existing != nil {
		fmt.Printf("User %q already exists.\n", username)
		os.Exit(1)
	}
	password := promptPassword()
	u := &store.User{Username: username, IsAdmin: isAdmin}
	if err := u.SetPassword(password); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if _, err := st.CreateUser(u); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	role := "customer"
	if isAdmin {
		role = "admin"
	}
	fmt.Printf("Created %s user %q.\n", role, username)
}

func manageSetPassword(st *store.Store, username string) {
	u, err := st.GetUserByUsername(username)
	if err != nil {
		fmt.Printf("No such user: %q\n", username)
		os.Exit(1)
	}
	if err := u.SetPassword(promptPassword()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := st.UpdateUserPassword(u); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("Password updated for %q.\n", username)
}

func manageListUsers(st *store.Store) {
	users, err := st.ListUsers()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, u := range users {
		role := "customer"
		if u.IsAdmin {
			role = "admin"
		}
		count, _ := st.CountServersByOwner(u.ID)
		fmt.Printf("%4d  %-20s %-10s %d server(s)\n", u.ID, u.Username, role, count)
	}
}

func manageDeleteUser(st *store.Store, username string) {
	u, err := st.GetUserByUsername(username)
	if err != nil {
		fmt.Printf("No such user: %q\n", username)
		os.Exit(1)
	}
	count, _ := st.CountServersByOwner(u.ID)
	if count > 0 {
		fmt.Println("Reassign or delete this user's servers first.")
		os.Exit(1)
	}
	if err := st.DeleteUser(u.ID); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("Deleted user %q.\n", username)
}

func runManage(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: panel manage <create-admin|create-user|set-password|list-users|delete-user> [username]")
		os.Exit(1)
	}
	_, st, _, _ := openDeps()
	defer st.Close()

	sub := args[0]
	rest := args[1:]
	needsUsername := sub != "list-users"
	var username string
	if needsUsername {
		if len(rest) < 1 {
			fmt.Fprintf(os.Stderr, "usage: panel manage %s <username>\n", sub)
			os.Exit(1)
		}
		username = rest[0]
	}

	switch sub {
	case "create-admin":
		manageCreateUser(st, username, true)
	case "create-user":
		manageCreateUser(st, username, false)
	case "set-password":
		manageSetPassword(st, username)
	case "list-users":
		manageListUsers(st)
	case "delete-user":
		manageDeleteUser(st, username)
	default:
		fmt.Fprintf(os.Stderr, "unknown manage subcommand: %s\n", sub)
		os.Exit(1)
	}
}
