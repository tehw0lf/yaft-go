package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// The generated shell is compiled and run, not just inspected: a module with
// an interface covering every awkward case, the generated file, and a test
// that exercises the shell.
func TestTheGeneratedShellCompilesAndReturnsNothing(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod": "module example.com/shellcheck\n\ngo 1.26.0\n",
		"api.go": `package api

import (
	"context"
	stdio "io"
	"time"
)

type closer interface{ Close() error }

// Service embeds a same-package interface and uses an aliased import.
type Service interface {
	closer
	Get(ctx context.Context, id string) (string, error)
	Stream() <-chan time.Time
	Both() chan int
	Send() chan<- int
	Sum(prefix string, xs ...int) int
	Reader() stdio.Reader
	Pair() (a, b int)
	Nothing()
}

type unexported interface{ Hidden() bool }
`,
		"api_test.go": `package api

import (
	"context"
	"testing"
	"time"
)

func TestShell(t *testing.T) {
	var s Service = NewNoopService()
	if v, err := s.Get(context.Background(), "x"); v != "" || err != nil {
		t.Errorf("Get = %q, %v", v, err)
	}
	for name, ch := range map[string]<-chan int{"both": s.Both()} {
		select {
		case _, open := <-ch:
			if open {
				t.Errorf("%s open", name)
			}
		case <-time.After(time.Second):
			t.Errorf("%s blocks", name)
		}
	}
	select {
	case _, open := <-s.Stream():
		if open {
			t.Error("Stream open")
		}
	case <-time.After(time.Second):
		t.Error("Stream blocks")
	}
	if s.Send() != nil || s.Sum("p", 1, 2) != 0 || s.Reader() != nil || s.Close() != nil {
		t.Error("zero values expected")
	}
	if a, b := s.Pair(); a != 0 || b != 0 {
		t.Error("Pair")
	}
	s.Nothing()
	var u unexported = newNoopUnexported()
	if u.Hidden() {
		t.Error("Hidden")
	}
}
`,
	})

	for _, typ := range []string{"Service", "unexported"} {
		src, err := generate(dir, typ)
		if err != nil {
			t.Fatalf("generate(%s): %v", typ, err)
		}
		writeFiles(t, dir, map[string]string{strings.ToLower(typ) + "_yaftshell.go": string(src)})
	}

	cmd := exec.Command("go", "vet", "./...")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go vet on the generated code: %v\n%s", err, out)
	}
	cmd = exec.Command("go", "test", "./...")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the generated shell misbehaves: %v\n%s", err, out)
	}
}

func TestRefusesWhatItCannotGenerateCorrectly(t *testing.T) {
	cases := map[string]struct {
		src, typ, want string
	}{
		"foreign embed": {"package p\nimport \"io\"\ntype S interface{ io.Reader }\n", "S", "only interfaces from the same package"},
		"generic":       {"package p\ntype S[T any] interface{ Get() T }\n", "S", "generic"},
		"not interface": {"package p\ntype S struct{}\n", "S", "not an interface"},
		"missing":       {"package p\ntype S interface{}\n", "Other", "not found"},
		"unknown alias": {"package p\ntype S interface{ Get() x.Thing }\n", "S", "cannot find the import"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, map[string]string{"p.go": c.src})
			_, err := generate(dir, c.typ)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want %q", err, c.want)
			}
		})
	}
}
