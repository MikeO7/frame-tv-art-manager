package samsung

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func secureTokenDirectory(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatalf("secure test token directory: %v", err)
	}
	return directory
}

func TestLoadAuthenticationTokenEnforcesSensitiveFileContract(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		prepare func(*testing.T) string
		want    string
	}{
		{
			name: "secure regular token",
			prepare: func(t *testing.T) string {
				return writeTokenFixture(t, 0o700, 0o600, "token-123")
			},
			want: "token-123",
		},
		{
			name: "permissive parent",
			prepare: func(t *testing.T) string {
				return writeTokenFixture(t, 0o755, 0o600, "token")
			},
		},
		{
			name: "permissive token",
			prepare: func(t *testing.T) string {
				return writeTokenFixture(t, 0o700, 0o644, "token")
			},
		},
		{
			name: "token symlink",
			prepare: func(t *testing.T) string {
				directory := t.TempDir()
				target := filepath.Join(directory, "target")
				if err := os.WriteFile(target, []byte("token"), 0o600); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(directory, "token")
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
				return path
			},
		},
		{
			name: "parent symlink",
			prepare: func(t *testing.T) string {
				root := t.TempDir()
				target := filepath.Join(root, "target")
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(target, "token"), []byte("token"), 0o600); err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(root, "link")
				if err := os.Symlink(target, link); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(link, "token")
			},
		},
		{
			name: "non-regular token",
			prepare: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "token")
				if err := os.Mkdir(path, 0o600); err != nil {
					t.Fatal(err)
				}
				return path
			},
		},
		{
			name: "unnormalized token",
			prepare: func(t *testing.T) string {
				return writeTokenFixture(t, 0o700, 0o600, " token\n")
			},
		},
		{
			name: "oversized token",
			prepare: func(t *testing.T) string {
				return writeTokenFixture(t, 0o700, 0o600, strings.Repeat("x", 4097))
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := test.prepare(t)
			got, err := loadAuthenticationToken(path)
			if test.want == "" {
				if err == nil || got != "" {
					t.Fatalf("loadAuthenticationToken() = %q, %v; want rejection", got, err)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("loadAuthenticationToken() = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestDryRunAndConnectionShareStrictTokenLoaderWithoutWrites(t *testing.T) {
	t.Parallel()

	path := writeTokenFixture(t, 0o700, 0o600, "stable-token")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	transport := &protocolTransport{config: Config{TokenPath: path}}
	if err := transport.admitConnection(context.Background(), "127.0.0.1", true, nil); err != nil {
		t.Fatalf("admitConnection() error = %v", err)
	}
	connection := newConnection(connConfig{tokenFile: path})
	got, err := connection.readToken()
	if err != nil || got != "stable-token" {
		t.Fatalf("readToken() = %q, %v", got, err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) || info.Mode().Perm() != 0o600 {
		t.Fatalf("dry-run token changed from %q to %q", before, after)
	}
}

func TestPersistAuthenticationTokenRejectsSymlinkState(t *testing.T) {
	t.Run("token file", func(t *testing.T) {
		directory := t.TempDir()
		if err := os.Chmod(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(directory, "target.txt")
		if err := os.WriteFile(target, []byte("old-token"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(directory, "token.txt")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if err := persistAuthenticationToken(context.Background(), link, "new-token"); err == nil {
			t.Fatal("persistAuthenticationToken accepted a symlink")
		}
		data, err := os.ReadFile(target)
		if err != nil || string(data) != "old-token" {
			t.Fatalf("symlink target changed: %q, %v", data, err)
		}
	})

	t.Run("token directory", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "target")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "tokens")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if err := persistAuthenticationToken(context.Background(), filepath.Join(link, "token.txt"), "new-token"); err == nil {
			t.Fatal("persistAuthenticationToken accepted a symlink directory")
		}
	})
}

func writeTokenFixture(t *testing.T, directoryMode, fileMode os.FileMode, token string) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "tokens")
	if err := os.Mkdir(directory, directoryMode); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "token")
	if err := os.WriteFile(path, []byte(token), fileMode); err != nil {
		t.Fatal(err)
	}
	return path
}
