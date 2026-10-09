package integration_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestACLByoiRequiresEveryStreamingService(t *testing.T) {
	for _, inactive := range []string{"", "acr-mirror", "overlaybd-tcmu", "overlaybd-snapshotter"} {
		t.Run("inactive="+inactive, func(t *testing.T) {
			dir := t.TempDir()
			mock := "#!/bin/sh\n[ \"$1 $2\" = 'is-active --quiet' ] || exit 9\n[ \"$3\" != \"$INACTIVE_SERVICE\" ]\n"
			if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(mock), 0755); err != nil {
				t.Fatal(err)
			}
			command := exec.Command("bash", "-eu", "-c", aclBYOIActiveServicesCheck)
			command.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "INACTIVE_SERVICE="+inactive)
			output, err := command.CombinedOutput()
			if inactive == "" {
				if err != nil || strings.Count(string(output), " active\n") != 3 {
					t.Fatalf("all active services must pass with three confirmations: %v %s", err, output)
				}
			} else if err == nil {
				t.Fatalf("inactive %s incorrectly passed: %s", inactive, output)
			}
		})
	}
}
