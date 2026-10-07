package internal_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Проверяет независимость проекта и направление зависимостей слоёв.
func TestServiceBoundariesAndDependencyDirection(t *testing.T) {
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range file.Imports {
			dependency, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			for _, forbidden := range []string{"github.com/KolibriRDM/catalog-service/", "github.com/KolibriRDM/notifications-service/", "github.com/KolibriRDM/food-delivery/internal/"} {
				if strings.HasPrefix(dependency, forbidden) {
					t.Errorf("%s imports another service: %s", path, dependency)
				}
			}
			inner := strings.HasPrefix(path, "model/") || strings.HasPrefix(path, "service/")
			infrastructure := strings.Contains(dependency, "/repository") || strings.Contains(dependency, "/handler") || strings.Contains(dependency, "/catalogclient") || strings.Contains(dependency, "/kafkapublisher") || strings.HasPrefix(dependency, "github.com/twmb/franz-go") || strings.Contains(dependency, "jackc/pgx") || dependency == "net/http" || strings.HasPrefix(dependency, "google.golang.org/grpc") || strings.HasPrefix(dependency, "google.golang.org/protobuf")
			if inner && infrastructure {
				t.Errorf("inner layer %s imports infrastructure: %s", path, dependency)
			}
			if strings.HasPrefix(path, "handler/") && (strings.Contains(dependency, "/repository") || strings.Contains(dependency, "jackc/pgx")) {
				t.Errorf("handler %s bypasses service: %s", path, dependency)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
