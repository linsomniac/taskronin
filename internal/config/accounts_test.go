package config_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"taskronin/internal/config"
)

func TestOtherHumanAccounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "passwd")
	fixture := "root:x:0:0::/root:/bin/bash\nme:x:1000:1000::/home/me:/bin/zsh\n" +
		"nobody:x:65534:65534::/:/usr/sbin/nologin\nbob:x:1001:1001::/home/bob:/bin/bash\n" +
		"svc:x:1002:1002::/:/bin/false\nempty:x:1003:1003::/:\nlow:x:999:999::/:/bin/bash\n"
	if err := os.WriteFile(path, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := config.OtherHumanAccounts(path, 1000)
	if err != nil || !reflect.DeepEqual(got, []string{"bob"}) {
		t.Fatalf("accounts = %v, %v", got, err)
	}
}

func TestOtherHumanAccountsErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "passwd")
	if _, err := config.OtherHumanAccounts(path, 1000); err == nil {
		t.Fatal("accepted unreadable account list")
	}
	for _, fixture := range []string{"malformed\n", "bob:x:unknown:1001::/:/bin/bash\n", strings.Repeat("x", 100000)} {
		if err := os.WriteFile(path, []byte(fixture), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := config.OtherHumanAccounts(path, 1000); err == nil {
			t.Fatal("accepted malformed or incomplete account list")
		}
	}
}
