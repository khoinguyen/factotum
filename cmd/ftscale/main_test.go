package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestParseScales(t *testing.T) {
	got, err := parseScales("1000, 10000,50000")
	if err != nil {
		t.Fatalf("parseScales() error = %v", err)
	}
	if want := []int{1000, 10000, 50000}; !reflect.DeepEqual(got, want) {
		t.Fatalf("parseScales() = %v, want %v", got, want)
	}
}

func TestParseScalesRejects(t *testing.T) {
	for _, value := range []string{"", "nope", "0", "-5"} {
		if _, err := parseScales(value); err == nil {
			t.Fatalf("parseScales(%q) error = nil, want error", value)
		}
	}
}

func TestBuildConfigRejectsEmptyBackends(t *testing.T) {
	if _, err := buildConfig("", "1000", 1, 0); err == nil {
		t.Fatal("buildConfig() error = nil, want error for no backends")
	}
}

func TestRunUsageErrorsExitTwo(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-tasks", "oops"}, &stdout, &stderr); code != 2 {
		t.Fatalf("run() code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "invalid scale") {
		t.Fatalf("stderr = %q, want invalid scale", stderr.String())
	}
}
