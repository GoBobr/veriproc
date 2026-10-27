package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/gobobr/veriproc/internal/accessprofile"
)

// --- subcommand: users ------------------------------------------------------
//
// `veriproc users` edits an access-profile file directly. It never contacts
// the API and never involves the web console: the profile file is the
// authority, so the same file that grants access decides who may grant access.
// Mutating subcommands require the caller's own credential to resolve to an
// operator entry in that file.

// resolveProfilePath picks the profile file for a users subcommand: the
// --profile flag wins, then VERIPROC_ACCESS_PROFILE. An empty result is a
// usage error, because guessing a path for a credential file would be worse
// than refusing.
func resolveProfilePath(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if v := strings.TrimSpace(os.Getenv("VERIPROC_ACCESS_PROFILE")); v != "" {
		return v, nil
	}
	return "", errors.New("no access profile: pass --profile PATH or set VERIPROC_ACCESS_PROFILE")
}

// authorizeProfileEdit enforces the operator gate for a mutating users
// subcommand. The caller's credential must resolve to an operator entry in
// the very file being edited; a missing credential, an unknown credential, or
// a reader entry all refuse. No API is consulted.
func authorizeProfileEdit(path string, token string) error {
	p, err := accessprofile.LoadFile(path)
	if err != nil {
		return err
	}
	if strings.TrimSpace(token) == "" {
		return errors.New("operator access is required: supply --token or VERIPROC_TOKEN with an operator credential for " + path)
	}
	entry, ok := p.Lookup(token)
	if !ok {
		return errors.New("operator access is required: this credential has no entry in " + path)
	}
	if entry.Role != accessprofile.RoleOperator {
		return fmt.Errorf("operator access is required: %q holds the %s role in %s", entry.Subject, entry.Role, path)
	}
	return nil
}

func (c *client) cmdUsers(sub string, args []string) int {
	switch sub {
	case "list":
		return c.usersList(args)
	case "add":
		return c.usersAdd(args)
	case "revoke":
		return c.usersRevoke(args)
	case "":
		fmt.Fprintln(c.stderr, "veriproc users list|add|revoke [--profile PATH]")
		return ExitUsage
	default:
		fmt.Fprintf(c.stderr, "veriproc: unknown users subcommand %q\n", sub)
		return ExitUsage
	}
}

// usersList prints one line per entry: subject, role, and label. Token values
// are never printed, in any output mode.
func (c *client) usersList(args []string) int {
	fs := flag.NewFlagSet("users list", flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	profile := fs.String("profile", "", "access profile file (default: $VERIPROC_ACCESS_PROFILE)")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	path, err := resolveProfilePath(*profile)
	if err != nil {
		fmt.Fprintln(c.stderr, "veriproc users list: "+err.Error())
		return ExitUsage
	}
	p, err := accessprofile.LoadFile(path)
	if err != nil {
		fmt.Fprintln(c.stderr, "veriproc users list: "+err.Error())
		return ExitGeneric
	}
	fmt.Fprintf(c.stdout, "profile: %s (%d entries)\n", path, p.Len())
	for _, e := range p.Entries() {
		label := e.Label
		if label == "" {
			label = "-"
		}
		fmt.Fprintf(c.stdout, "%-24s %-8s %s\n", e.Subject, e.Role, label)
	}
	return ExitOK
}

// usersAdd mints a token, stores only its digest in the profile, and prints
// the plaintext token exactly once.
func (c *client) usersAdd(args []string) int {
	fs := flag.NewFlagSet("users add", flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	profile := fs.String("profile", "", "access profile file (default: $VERIPROC_ACCESS_PROFILE)")
	label := fs.String("label", "", "human-readable label for the new entry")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	rest := fs.Args()
	if len(rest) != 2 {
		fmt.Fprintln(c.stderr, "veriproc users add [--profile PATH] [--label LABEL] SUBJECT ROLE")
		return ExitUsage
	}
	subject, roleRaw := rest[0], rest[1]
	role, err := accessprofile.ParseRole(roleRaw)
	if err != nil {
		fmt.Fprintln(c.stderr, "veriproc users add: "+err.Error())
		return ExitUsage
	}
	path, err := resolveProfilePath(*profile)
	if err != nil {
		fmt.Fprintln(c.stderr, "veriproc users add: "+err.Error())
		return ExitUsage
	}
	if err := authorizeProfileEdit(path, c.token); err != nil {
		fmt.Fprintln(c.stderr, "veriproc users add: "+err.Error())
		return ExitAuth
	}
	p, err := accessprofile.LoadFile(path)
	if err != nil {
		fmt.Fprintln(c.stderr, "veriproc users add: "+err.Error())
		return ExitGeneric
	}
	token, err := accessprofile.MintToken()
	if err != nil {
		fmt.Fprintln(c.stderr, "veriproc users add: "+err.Error())
		return ExitGeneric
	}
	next, err := p.Append(accessprofile.NewEntry(subject, role, *label, token))
	if err != nil {
		fmt.Fprintln(c.stderr, "veriproc users add: "+err.Error())
		return ExitValidation
	}
	if err := accessprofile.Save(path, next); err != nil {
		fmt.Fprintln(c.stderr, "veriproc users add: "+err.Error())
		return ExitGeneric
	}
	// The plaintext token is printed exactly once, here, and never stored.
	fmt.Fprintf(c.stdout, "added %s (%s) to %s\n", subject, role, path)
	fmt.Fprintf(c.stdout, "token (shown once, store it now): %s\n", token)
	return ExitOK
}

// usersRevoke removes every entry bound to a subject.
func (c *client) usersRevoke(args []string) int {
	fs := flag.NewFlagSet("users revoke", flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	profile := fs.String("profile", "", "access profile file (default: $VERIPROC_ACCESS_PROFILE)")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(c.stderr, "veriproc users revoke [--profile PATH] SUBJECT")
		return ExitUsage
	}
	subject := rest[0]
	path, err := resolveProfilePath(*profile)
	if err != nil {
		fmt.Fprintln(c.stderr, "veriproc users revoke: "+err.Error())
		return ExitUsage
	}
	if err := authorizeProfileEdit(path, c.token); err != nil {
		fmt.Fprintln(c.stderr, "veriproc users revoke: "+err.Error())
		return ExitAuth
	}
	p, err := accessprofile.LoadFile(path)
	if err != nil {
		fmt.Fprintln(c.stderr, "veriproc users revoke: "+err.Error())
		return ExitGeneric
	}
	next, err := p.Revoke(subject)
	if err != nil {
		fmt.Fprintln(c.stderr, "veriproc users revoke: "+err.Error())
		return ExitValidation
	}
	if err := accessprofile.Save(path, next); err != nil {
		fmt.Fprintln(c.stderr, "veriproc users revoke: "+err.Error())
		return ExitGeneric
	}
	fmt.Fprintf(c.stdout, "revoked %s from %s\n", subject, path)
	return ExitOK
}