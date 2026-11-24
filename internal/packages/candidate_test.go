package packages

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCandidateArchive(t *testing.T) {
	archive := os.Getenv("LENV_TEST_PACKAGE_ARCHIVE")
	if archive == "" {
		t.Skip("set LENV_TEST_PACKAGE_ARCHIVE to verify a local Redis or MariaDB candidate")
	}
	root, ok := strings.CutSuffix(filepath.Base(archive), ".tar.gz")
	if !ok {
		t.Fatal("candidate filename must end in .tar.gz")
	}
	service, _, _ := strings.Cut(root, "-")
	if service != "redis" && service != "mariadb" {
		t.Fatal("expected a Redis or MariaDB candidate")
	}
	dest := filepath.Join(t.TempDir(), "files")
	if err := extract(context.Background(), archive, dest, root); err != nil {
		t.Fatal(err)
	}
	if err := checkTools(service, filepath.Join(dest, "bin")); err != nil {
		t.Fatal(err)
	}
}
