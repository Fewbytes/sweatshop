// Package secret resolves named line secrets (the model token, the Discord
// token) from the places a deployment can keep them, so no unit has to `cat`
// a file into an environment variable.
package secret

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Secret names. Each is also the file name inside the line directory and the
// credential name in a systemd LoadCredential= line.
const (
	ModelCred   = "model.cred"
	DiscordCred = "discord.cred"
	// S3AccessKeyCred and S3SecretKeyCred are the report store's S3 credential.
	S3AccessKeyCred = "s3-access-key.cred"
	S3SecretKeyCred = "s3-secret-key.cred"
)

// ErrNotFound means a source does not hold the named secret. Any other error
// means the source holds it but it is unusable (bad mode, empty).
var ErrNotFound = errors.New("secret not found")

// Source yields the value of one named secret. Errors never contain the value.
type Source interface {
	Read(name string) (string, error)
}

// File reads Dir/name and insists on mode 0600 or stricter.
type File struct{ Dir string }

// Read implements Source.
func (f File) Read(name string) (string, error) {
	path := filepath.Join(f.Dir, name)
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s has mode %o; want 0600", path, info.Mode().Perm())
	}
	return readTrimmed(path)
}

// Env reads the environment variable Vars[name]. An unset or empty variable
// counts as absent, so a unit can blank an inherited value.
type Env struct{ Vars map[string]string }

// Read implements Source.
func (e Env) Read(name string) (string, error) {
	v := strings.TrimSpace(os.Getenv(e.Vars[name]))
	if e.Vars[name] == "" || v == "" {
		return "", ErrNotFound
	}
	return v, nil
}

// SystemdCredential reads name from the directory systemd names in
// $CREDENTIALS_DIRECTORY (set for units with LoadCredential=).
type SystemdCredential struct{}

// Read implements Source.
func (SystemdCredential) Read(name string) (string, error) {
	dir := os.Getenv("CREDENTIALS_DIRECTORY")
	if dir == "" {
		return "", ErrNotFound
	}
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return "", ErrNotFound
	}
	return readTrimmed(path)
}

// Chain asks each source in order and returns the first value found.
type Chain []Source

// Read implements Source. It returns ErrNotFound when no source has the secret.
func (c Chain) Read(name string) (string, error) {
	for _, s := range c {
		v, err := s.Read(name)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		return v, err
	}
	return "", ErrNotFound
}

// ForLine is the line's standard lookup order: systemd credential, then
// environment, then a file in the line directory.
func ForLine(lineDir string) Source {
	return Chain{
		SystemdCredential{},
		Env{Vars: map[string]string{
			ModelCred:       "CLAUDE_CODE_OAUTH_TOKEN",
			DiscordCred:     "SWEATSHOP_DISCORD_TOKEN",
			S3AccessKeyCred: "AWS_ACCESS_KEY_ID",
			S3SecretKeyCred: "AWS_SECRET_ACCESS_KEY",
		}},
		File{Dir: lineDir},
	}
}

func readTrimmed(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		return "", fmt.Errorf("%s is empty", path)
	}
	return v, nil
}
