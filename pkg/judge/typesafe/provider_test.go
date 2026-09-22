package typesafe

import (
	"testing"

	"github.com/khoinguyen/factotum/pkg/judge"
)

func TestRegisteredAsProvider(t *testing.T) {
	j, err := judge.New("typesafe", func(string) string { return "" },
		map[string]string{"secret_api_key": "sk-1"})
	if err != nil {
		t.Fatalf("typesafe is not registered: %v", err)
	}
	if _, disabled := j.(judge.Disabled); disabled {
		t.Fatal("typesafe with a key should build a client")
	}
}

func TestFromOptionsNoKeyIsDisabled(t *testing.T) {
	j, err := fromOptions(func(string) string { return "" }, nil)
	if err != nil {
		t.Fatalf("fromOptions() error = %v", err)
	}
	if _, disabled := j.(judge.Disabled); !disabled {
		t.Fatal("no key should be Disabled")
	}
}

func TestFromOptionsUsesConfigKey(t *testing.T) {
	j, err := fromOptions(func(string) string { return "" },
		map[string]string{"secret_api_key": "cfg-key"})
	if err != nil {
		t.Fatalf("fromOptions() error = %v", err)
	}
	if _, disabled := j.(judge.Disabled); disabled {
		t.Fatal("config key should build a client")
	}
}

func TestFromOptionsEnvWins(t *testing.T) {
	getenv := func(key string) string {
		if key == "TYPESAFE_API_KEY" {
			return "env-key"
		}
		return ""
	}
	j, err := fromOptions(getenv, nil)
	if err != nil {
		t.Fatalf("fromOptions() error = %v", err)
	}
	if _, disabled := j.(judge.Disabled); disabled {
		t.Fatal("environment key should build a client")
	}
}
