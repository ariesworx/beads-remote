// Package remote connects a repository to its database on a shared, remote
// beads (bd) Dolt server, and provisions that database on the server.
//
// Security model:
//   - The SSH key is the only credential. The server's host key is pinned in
//     the repository config and checked on every connection; it is never
//     trusted on first use.
//   - Each key in the database account's authorized_keys is forced to one
//     command (print this database's password) and may forward only to the
//     server's MySQL port. No shell, no pty, no agent forwarding.
//   - The password is cached at 0600 outside the repository and written to
//     bd's credentials file (0600). It is never printed and never passed as a
//     command-line argument.
//   - Nothing touches the user's ~/.ssh: keys pinned and options passed here
//     live under ~/.config/beads-remote.
package remote

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// ConfigFile is the committed, per-repository configuration.
const ConfigFile = ".beads/remote.yaml"

// Config names one database on one server.
type Config struct {
	Server struct {
		Host    string `yaml:"host"`     // DNS name or IP of the server
		HostKey string `yaml:"host_key"` // pinned ED25519 fingerprint, SHA256:…
		Admin   string `yaml:"admin"`    // ssh destination with passwordless sudo, for `server` commands
		SSHPort int    `yaml:"ssh_port"` // default 22
	} `yaml:"server"`
	Database string `yaml:"database"` // Dolt database, Unix account and MySQL user
	Port     int    `yaml:"port"`     // local end of the tunnel
	Prefix   string `yaml:"prefix"`   // issue-id prefix; default Database

	// Paths on the server. The defaults match scripts/bootstrap.sh.
	Paths struct {
		PasswordFile      string `yaml:"password_file"`       // this database's password; default /etc/{database}/db-password
		AdminUser         string `yaml:"admin_user"`          // MySQL admin user; default "beads"
		AdminPasswordFile string `yaml:"admin_password_file"` // default /etc/beads/db-password
		SSHDConfig        string `yaml:"sshd_config"`         // default /etc/ssh/sshd_config.d/99-beads-hardening.conf
		BackupScript      string `yaml:"backup_script"`       // default /usr/local/sbin/dolt-backup.sh
	} `yaml:"paths"`
}

var (
	nameRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,30}$`)
	hostRE = regexp.MustCompile(`^[A-Za-z0-9.-]{1,253}$`)
	destRE = regexp.MustCompile(`^([a-z_][a-z0-9_-]{0,31}@)?[A-Za-z0-9.-]{1,253}$`)
	pathRE = regexp.MustCompile(`^/[A-Za-z0-9/._-]+$`)
	fprRE  = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)
)

// Load finds .beads/remote.yaml in dir or a parent, and returns the config
// and the repository root.
func Load(dir string) (*Config, string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, "", err
	}
	for {
		b, err := os.ReadFile(filepath.Join(dir, ConfigFile))
		if err == nil {
			c, err := Parse(b)
			return c, dir, err
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, "", err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, "", fmt.Errorf("%s not found here or in any parent (run `beads-remote init`)", ConfigFile)
		}
		dir = parent
	}
}

// Parse reads and validates a config, filling defaults.
func Parse(b []byte) (*Config, error) {
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", ConfigFile, err)
	}
	c.defaults()
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", ConfigFile, err)
	}
	return &c, nil
}

func (c *Config) defaults() {
	if c.Server.SSHPort == 0 {
		c.Server.SSHPort = 22
	}
	if c.Server.Admin == "" {
		c.Server.Admin = c.Server.Host
	}
	if c.Prefix == "" {
		c.Prefix = c.Database
	}
	if c.Paths.PasswordFile == "" {
		c.Paths.PasswordFile = "/etc/{database}/db-password"
	}
	c.Paths.PasswordFile = strings.ReplaceAll(c.Paths.PasswordFile, "{database}", c.Database)
	if c.Paths.AdminUser == "" {
		c.Paths.AdminUser = "beads"
	}
	if c.Paths.AdminPasswordFile == "" {
		c.Paths.AdminPasswordFile = "/etc/beads/db-password"
	}
	if c.Paths.SSHDConfig == "" {
		c.Paths.SSHDConfig = "/etc/ssh/sshd_config.d/99-beads-hardening.conf"
	}
	if c.Paths.BackupScript == "" {
		c.Paths.BackupScript = "/usr/local/sbin/dolt-backup.sh"
	}
}

// Validate rejects anything unsafe to put on a command line or in a remote
// script. Every value that reaches the server passes through here.
func (c *Config) Validate() error {
	switch {
	case !hostRE.MatchString(c.Server.Host):
		return fmt.Errorf("server.host %q is not a host name", c.Server.Host)
	case !fprRE.MatchString(c.Server.HostKey):
		return fmt.Errorf("server.host_key must be the server's ED25519 fingerprint (SHA256:…, from `ssh-keyscan -t ed25519 HOST | ssh-keygen -lf -`)")
	case !destRE.MatchString(c.Server.Admin):
		return fmt.Errorf("server.admin %q is not an ssh destination", c.Server.Admin)
	case c.Server.SSHPort < 1 || c.Server.SSHPort > 65535:
		return fmt.Errorf("server.ssh_port %d is out of range", c.Server.SSHPort)
	case !nameRE.MatchString(c.Database):
		return fmt.Errorf("database %q must be lowercase letters, digits and _, starting with a letter", c.Database)
	case !regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`).MatchString(c.Prefix):
		return fmt.Errorf("prefix %q must be lowercase letters, digits and -", c.Prefix)
	case c.Port < 1024 || c.Port > 65535:
		return fmt.Errorf("port %d must be between 1024 and 65535", c.Port)
	case !nameRE.MatchString(c.Paths.AdminUser):
		return fmt.Errorf("paths.admin_user %q is not a plain name", c.Paths.AdminUser)
	}
	for _, p := range []string{c.Paths.PasswordFile, c.Paths.AdminPasswordFile, c.Paths.SSHDConfig, c.Paths.BackupScript} {
		if !pathRE.MatchString(p) || strings.Contains(p, "..") {
			return fmt.Errorf("path %q must be absolute, with no spaces and no .. component", p)
		}
	}
	return nil
}

// Derived names. One database is one Unix account and one MySQL user.
func (c *Config) user() string     { return c.Database }
func (c *Config) remotePW() string { return c.Paths.PasswordFile }
func (c *Config) dest() string     { return c.user() + "@" + c.Server.Host }

// Template is the config `init` writes, with comments.
const Template = `# beads-remote: this repository's database on a shared beads (Dolt) server.
# Committed: it holds no secrets. See https://github.com/ariesworx/beads-remote
server:
  host: %s
  # The server's ED25519 host key, verified out of band. Every connection is
  # checked against it; a different key is refused.
  host_key: %s
  # ssh destination with passwordless sudo, used only by "server" commands.
  # admin: admin@%s
database: %s
# Local end of the tunnel. Give each repository on this machine its own port.
port: %d
prefix: %s
`
