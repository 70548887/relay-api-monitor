package launcher

import (
	"strings"
	"testing"
)

func TestResolvedPublicURLIsForwardedToBothChildrenWithoutNewCLIArguments(t *testing.T) {
	for _, value := range []string{"https://configured.example:8443", ""} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("RELAYLAB_PUBLIC_URL", "https://inherited.example")
			s := newProcessSupervisor(t)
			s.opts.PublicURL = &value
			startFixture(t, s)
			for _, child := range s.children() {
				count := 0
				for _, variable := range child.cmd.Env {
					key, actual, _ := strings.Cut(variable, "=")
					if key == "RELAYLAB_PUBLIC_URL" {
						count++
						if actual != value {
							t.Fatalf("%s public origin was not overridden", child.role)
						}
					}
				}
				if count != 1 {
					t.Fatalf("%s has %d public origin environment entries", child.role, count)
				}
				for _, argument := range child.cmd.Args {
					if argument == "--public-url" {
						t.Fatal("new flag would break older rollback binaries")
					}
				}
			}
		})
	}
}
