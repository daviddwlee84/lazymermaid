// Package config implements explicit XDG configuration without side effects.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	Editor  Editor  `toml:"editor" json:"editor"`
	Render  Render  `toml:"render" json:"render"`
	Preview Preview `toml:"preview" json:"preview"`
	Scan    Scan    `toml:"scan" json:"scan"`
	Path    string  `toml:"-" json:"config_path"`
}
type Editor struct {
	Command      string   `toml:"command" json:"command"`
	RuntimePaths []string `toml:"runtime_paths" json:"runtime_paths"`
}
type Render struct {
	Node           string `toml:"node" json:"node"`
	RuntimeDir     string `toml:"runtime_dir" json:"runtime_dir"`
	Termaid        string `toml:"termaid" json:"termaid"`
	DebounceMS     int    `toml:"debounce_ms" json:"debounce_ms"`
	TimeoutSeconds int    `toml:"timeout_seconds" json:"timeout_seconds"`
	MermaidConfig  string `toml:"mermaid_config" json:"mermaid_config,omitempty"`
}
type Preview struct {
	Mode  string `toml:"mode" json:"mode"`
	Theme string `toml:"theme" json:"theme"`
}
type Scan struct {
	Hidden   bool `toml:"hidden" json:"hidden"`
	NoIgnore bool `toml:"no_ignore" json:"no_ignore"`
}

func XDG(env, fallback string) string {
	if p := os.Getenv(env); filepath.IsAbs(p) {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, fallback)
}
func CacheDir() string { return filepath.Join(XDG("XDG_CACHE_HOME", ".cache"), "lazymermaid") }
func DataDir() string  { return filepath.Join(XDG("XDG_DATA_HOME", ".local/share"), "lazymermaid") }
func DefaultPath() string {
	return filepath.Join(XDG("XDG_CONFIG_HOME", ".config"), "lazymermaid", "config.toml")
}
func Default() Config {
	runtimeDir := filepath.Join(DataDir(), "runtime", "mermaid")
	// A checkout is a supported installation source. Never load project config implicitly.
	if p, err := filepath.Abs("runtime/mermaid"); err == nil {
		if _, err = os.Stat(filepath.Join(p, "package.json")); err == nil {
			runtimeDir = p
		}
	}
	return Config{Editor: Editor{Command: "nvim", RuntimePaths: []string{}}, Render: Render{Node: "node", RuntimeDir: runtimeDir, Termaid: "termaid", DebounceMS: 300, TimeoutSeconds: 30}, Preview: Preview{Mode: "auto", Theme: "dark"}, Path: DefaultPath()}
}
func Load(path string) (Config, error) {
	c := Default()
	explicit := path != ""
	if explicit {
		c.Path = path
	}
	b, err := os.ReadFile(c.Path)
	if err == nil {
		if err = toml.Unmarshal(b, &c); err != nil {
			return c, fmt.Errorf("config %s: %w", c.Path, err)
		}
	} else if explicit || !os.IsNotExist(err) {
		return c, err
	}
	for name, dst := range map[string]*string{"LAZYMERMAID_NVIM": &c.Editor.Command, "LAZYMERMAID_NODE": &c.Render.Node, "LAZYMERMAID_RUNTIME_DIR": &c.Render.RuntimeDir, "LAZYMERMAID_TERMAID": &c.Render.Termaid, "LAZYMERMAID_PREVIEW": &c.Preview.Mode, "LAZYMERMAID_THEME": &c.Preview.Theme} {
		if v, ok := os.LookupEnv(name); ok {
			*dst = v
		}
	}
	return c, c.Validate()
}
func (c Config) Validate() error {
	switch c.Preview.Mode {
	case "auto", "image", "unicode", "ascii":
	default:
		return fmt.Errorf("preview.mode must be auto, image, unicode, or ascii")
	}
	if c.Render.DebounceMS < 0 || c.Render.TimeoutSeconds <= 0 {
		return fmt.Errorf("render.debounce_ms must be nonnegative and timeout_seconds positive")
	}
	if strings.TrimSpace(c.Editor.Command) == "" || strings.TrimSpace(c.Render.Node) == "" || strings.TrimSpace(c.Render.Termaid) == "" {
		return fmt.Errorf("backend commands cannot be empty")
	}
	if c.Render.MermaidConfig != "" && !json.Valid([]byte(c.Render.MermaidConfig)) {
		return fmt.Errorf("render.mermaid_config must contain valid JSON")
	}
	return nil
}
func (c Config) Timeout() time.Duration { return time.Duration(c.Render.TimeoutSeconds) * time.Second }
