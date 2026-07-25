package tui

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	configRoot, err := os.MkdirTemp("", "tagteam-tui-config-*")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("HOME", configRoot)
	_ = os.Setenv("XDG_CONFIG_HOME", configRoot)
	code := m.Run()
	_ = os.RemoveAll(configRoot)
	os.Exit(code)
}
