package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
	"slices"

	"github.com/jeon-jihyeon/nodloop/internal/mcp"
	"github.com/jeon-jihyeon/nodloop/internal/userconfig"
)

// One key a service calls the server with
// Only the SHA-256 of the key is saved so config.json never holds a key
type serverKey struct {
	// The person or service the key names
	// Approvals through the key are recorded under it
	Name   string   `json:"name"`
	Tenant string   `json:"tenant"`
	Role   mcp.Role `json:"role"`
	SHA256 string   `json:"sha256"`
}

// What the server section of config.json holds
type serverConfig struct {
	Keys []serverKey `json:"keys,omitempty"`
}

// A tenant names a directory under the record directory so it is one path element
var tenantName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// The key whose hash matches the token compared in constant time
func (c serverConfig) match(token string) (serverKey, bool) {
	sum := sha256.Sum256([]byte(token))
	got := hex.EncodeToString(sum[:])
	for _, k := range c.Keys {
		if subtle.ConstantTimeCompare([]byte(k.SHA256), []byte(got)) == 1 {
			return k, true
		}
	}
	return serverKey{}, false
}

func (c serverConfig) has(name string) bool {
	for _, k := range c.Keys {
		if k.Name == name {
			return true
		}
	}
	return false
}

func (c serverConfig) without(name string) serverConfig {
	keys := make([]serverKey, 0, len(c.Keys))
	for _, k := range c.Keys {
		if k.Name != name {
			keys = append(keys, k)
		}
	}
	c.Keys = keys
	return c
}

// key add, list and remove manage the keys serve accepts
func runKey(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return fail(stderr, "key", fmt.Errorf("%w %q", errUnknownAction, ""))
	}
	h := userconfig.Home(getenv("HOME"))
	if h == "" {
		return fail(stderr, "key", errHomeUnknown)
	}
	fs := newFlagSet("key "+args[0], stderr)
	tenant := fs.String("tenant", "", "add: the tenant whose records the key reads and writes")
	role := fs.String("role", "", "add: producer, reviewer or approver")
	name, err := parseName(fs, args[1:])
	if err != nil {
		return parseFailed(err)
	}
	var uc fileConfig
	if err := h.Read(&uc); err != nil {
		return fail(stderr, "key", err)
	}
	cmd := serverCommand{home: h, keys: uc.Server, out: stdout}
	switch args[0] {
	case "add":
		err = cmd.add(serverKey{Name: name, Tenant: *tenant, Role: mcp.Role(*role)})
	case "list":
		cmd.list()
	case "remove":
		err = cmd.remove(name)
	default:
		err = fmt.Errorf("%w %q", errUnknownAction, args[0])
	}
	if err != nil {
		return fail(stderr, "key", err)
	}
	return 0
}

type serverCommand struct {
	home userconfig.Home
	keys serverConfig
	out  io.Writer
}

// Prints the new key once and saves its hash
// The name is checked against the keys read under the config lock so two adds of one name never both land
func (c serverCommand) add(k serverKey) error {
	switch {
	case k.Name == "":
		return fmt.Errorf("add: a key name %w", errRequired)
	case !tenantName.MatchString(k.Tenant):
		return fmt.Errorf("%w: %q. Use lower case letters, digits, - and _", errTenantInvalid, k.Tenant)
	case !k.Role.Valid():
		return fmt.Errorf("%w: %q. Use one of %v", errRoleInvalid, k.Role, mcp.Roles())
	}
	var secret [32]byte
	// crypto rand Read never returns an error
	_, _ = rand.Read(secret[:])
	key := "nl_" + hex.EncodeToString(secret[:])
	sum := sha256.Sum256([]byte(key))
	k.SHA256 = hex.EncodeToString(sum[:])
	err := c.update(func(keys serverConfig) (serverConfig, error) {
		if keys.has(k.Name) {
			return keys, fmt.Errorf("%w: %s", errKeyExists, k.Name)
		}
		keys.Keys = append(slices.Clone(keys.Keys), k)
		return keys, nil
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(c.out, key)
	return nil
}

func (c serverCommand) list() {
	for _, k := range c.keys.Keys {
		fmt.Fprintf(c.out, "%s\t%s\t%s\n", k.Name, k.Tenant, k.Role)
	}
}

func (c serverCommand) remove(name string) error {
	return c.update(func(keys serverConfig) (serverConfig, error) {
		if !keys.has(name) {
			return keys, fmt.Errorf("%w: %s", errKeyUnknown, name)
		}
		return keys.without(name), nil
	})
}

// Rewrites the server key of config.json from the keys read under the config lock and keeps every other key
func (c serverCommand) update(change func(serverConfig) (serverConfig, error)) error {
	return c.home.Update(func(read func(string, any) error) (map[string]any, error) {
		var keys serverConfig
		if err := read("server", &keys); err != nil {
			return nil, err
		}
		keys, err := change(keys)
		if err != nil {
			return nil, err
		}
		return map[string]any{"server": keys}, nil
	})
}
