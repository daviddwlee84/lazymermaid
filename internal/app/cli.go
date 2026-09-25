package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	"lazymermaid/internal/config"
	"lazymermaid/internal/document"
	"lazymermaid/internal/handbook"
	"lazymermaid/internal/render"
	"lazymermaid/internal/tui"
)

type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }
func failure(err error) error {
	if err == nil {
		return nil
	}
	return &exitError{1, err}
}
func usage(err error) error { return &exitError{2, err} }
func ExitCode(err error) int {
	if errors.Is(err, context.Canceled) {
		return 130
	}
	var e *exitError
	if errors.As(err, &e) {
		return e.code
	}
	return 2
}
func Execute(ctx context.Context, args []string, version string) error {
	c := NewCommand(version)
	c.SetArgs(args)
	return c.ExecuteContext(ctx)
}
func NewCommand(version string) *cobra.Command {
	var configPath, theme, preview, color string
	root := &cobra.Command{Use: "lazymermaid [path]", Short: "Explore, edit and preview Mermaid diagrams in your terminal", Version: version, Args: cobra.MaximumNArgs(1), SilenceUsage: true, SilenceErrors: true}
	root.SetVersionTemplate("lazymermaid {{.Version}}\n")
	root.PersistentFlags().StringVar(&configPath, "config", "", "Explicit TOML configuration file")
	root.PersistentFlags().StringVar(&theme, "theme", "", "Mermaid theme override")
	root.PersistentFlags().StringVar(&preview, "preview", "", "Preview mode: auto, image, unicode, ascii")
	root.PersistentFlags().StringVar(&color, "color", "auto", "Color: auto, always, never")
	load := func(cmd *cobra.Command) (config.Config, error) {
		if color != "auto" && color != "always" && color != "never" {
			return config.Config{}, usage(fmt.Errorf("invalid --color %q", color))
		}
		c, e := config.Load(configPath)
		if e != nil {
			return c, usage(e)
		}
		if cmd.Flags().Changed("theme") {
			c.Preview.Theme = theme
		}
		if cmd.Flags().Changed("preview") {
			c.Preview.Mode = preview
		}
		return c, func() error {
			if e = c.Validate(); e != nil {
				return usage(e)
			}
			return nil
		}()
	}
	root.RunE = func(cmd *cobra.Command, args []string) error {
		c, e := load(cmd)
		if e != nil {
			return e
		}
		if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
			if len(args) == 0 {
				return cmd.Help()
			}
			return usage(fmt.Errorf("the dashboard requires an input and output TTY; use scan or render for pipelines"))
		}
		path := "."
		if len(args) > 0 {
			path = args[0]
		}
		return failure(tui.Run(cmd.Context(), c, path, color))
	}
	var scanJSON, hidden, noIgnore bool
	scan := &cobra.Command{Use: "scan [path]", Short: "List Mermaid files and Markdown blocks", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c, e := load(cmd)
		if e != nil {
			return e
		}
		if cmd.Flags().Changed("hidden") {
			c.Scan.Hidden = hidden
		}
		if cmd.Flags().Changed("no-ignore") {
			c.Scan.NoIgnore = noIgnore
		}
		path := "."
		if len(args) > 0 {
			path = args[0]
		}
		ds, issues, e := document.Scan(cmd.Context(), path, document.ScanOptions{Hidden: c.Scan.Hidden, NoIgnore: c.Scan.NoIgnore})
		if e != nil {
			return failure(e)
		}
		if scanJSON {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
				Diagrams []document.Diagram   `json:"diagrams"`
				Issues   []document.ScanIssue `json:"issues"`
			}{ds, issues})
		}
		for _, d := range ds {
			fmt.Fprintf(cmd.OutOrStdout(), "%s:%d\t%s\n", d.Path, d.StartLine, d.Title)
		}
		for _, i := range issues {
			fmt.Fprintf(cmd.ErrOrStderr(), "%s: %s\n", i.Path, i.Message)
		}
		return nil
	}}
	scan.Flags().BoolVar(&scanJSON, "json", false, "Machine-readable output")
	scan.Flags().BoolVar(&hidden, "hidden", false, "Include hidden files")
	scan.Flags().BoolVar(&noIgnore, "no-ignore", false, "Include ignored files")
	root.AddCommand(scan)
	var format, out string
	var line int
	renderCmd := &cobra.Command{Use: "render <file|->", Short: "Render source to svg, png, unicode or ascii", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !validFormat(format) {
			return usage(fmt.Errorf("--format must be svg, png, unicode, or ascii"))
		}
		if cmd.Flags().Changed("line") && line <= 0 {
			return usage(fmt.Errorf("--line must be positive"))
		}
		c, e := load(cmd)
		if e != nil {
			return e
		}
		source, e := readSource(cmd, args[0], line)
		if e != nil {
			return usage(e)
		}
		if format == "png" && out == "" && term.IsTerminal(int(os.Stdout.Fd())) {
			return usage(fmt.Errorf("PNG output requires --output when stdout is a terminal"))
		}
		s := NewRenderer(c)
		defer s.Close()
		r, e := s.Render(cmd.Context(), render.Request{Source: source, Format: format, Theme: c.Preview.Theme, Config: json.RawMessage(c.Render.MermaidConfig)})
		if e != nil {
			return failure(e)
		}
		if out != "" {
			return failure(os.WriteFile(out, r.Data, 0644))
		}
		_, e = cmd.OutOrStdout().Write(r.Data)
		return failure(e)
	}}
	renderCmd.Flags().StringVarP(&format, "format", "f", "svg", "svg, png, unicode, ascii")
	renderCmd.Flags().StringVarP(&out, "output", "o", "", "Output file (otherwise stdout)")
	renderCmd.Flags().IntVar(&line, "line", 0, "Select a Markdown block containing this 1-based line")
	root.AddCommand(renderCmd)
	var checkJSON bool
	check := &cobra.Command{Use: "check [path]", Short: "Validate diagrams with the official Mermaid engine", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c, e := load(cmd)
		if e != nil {
			return e
		}
		path := "."
		if len(args) > 0 {
			path = args[0]
		}
		ds, issues, e := document.Scan(cmd.Context(), path, document.ScanOptions{Hidden: c.Scan.Hidden, NoIgnore: c.Scan.NoIgnore})
		if e != nil {
			return failure(e)
		}
		type checked struct {
			Path        string              `json:"path"`
			Line        int                 `json:"line"`
			Valid       bool                `json:"valid"`
			Diagnostics []render.Diagnostic `json:"diagnostics,omitempty"`
			Error       string              `json:"error,omitempty"`
		}
		results := make([]checked, 0, len(ds))
		s := NewRenderer(c)
		defer s.Close()
		bad := len(issues) > 0
		for _, d := range ds {
			r, e := s.Validate(cmd.Context(), d.Source)
			v := checked{Path: d.Path, Line: d.StartLine, Valid: e == nil, Diagnostics: r.Diagnostics}
			if e != nil {
				v.Error = e.Error()
				bad = true
			}
			results = append(results, v)
			if !checkJSON {
				status := "valid"
				if e != nil {
					status = v.Error
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s:%d\t%s\n", d.Path, d.StartLine, status)
			}
		}
		if checkJSON {
			if e = json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
				Results []checked            `json:"results"`
				Issues  []document.ScanIssue `json:"issues"`
			}{results, issues}); e != nil {
				return failure(e)
			}
		}
		if bad {
			return failure(fmt.Errorf("some diagrams could not be validated"))
		}
		return nil
	}}
	check.Flags().BoolVar(&checkJSON, "json", false, "Machine-readable output")
	root.AddCommand(check)
	var doctorJSON bool
	doctor := &cobra.Command{Use: "doctor", Short: "Inspect optional capabilities and installation instructions", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		c, e := load(cmd)
		if e != nil {
			return e
		}
		report := Doctor(cmd.Context(), c)
		if doctorJSON {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
		}
		for _, r := range report {
			fmt.Fprintf(cmd.OutOrStdout(), "%-18s %-12s %s\n", r.Name, r.Status, r.Detail)
			if r.Hint != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", r.Hint)
			}
		}
		return nil
	}}
	doctor.Flags().BoolVar(&doctorJSON, "json", false, "Machine-readable output")
	root.AddCommand(doctor)
	runtimeCmd := &cobra.Command{Use: "runtime", Short: "Prepare optional local renderer dependencies"}
	var runtimeDir string
	prepare := &cobra.Command{Use: "prepare", Short: "Write pinned npm manifests; does not install or download", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		c, e := load(cmd)
		if e != nil {
			return e
		}
		dir := runtimeDir
		if dir == "" {
			dir = c.Render.RuntimeDir
		}
		if e = render.PrepareRuntime(dir); e != nil {
			return failure(e)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Prepared %s\nInstall dependencies: npm ci --prefix %q\nInstall browser: npm exec --prefix %q -- puppeteer browsers install chrome\n", dir, dir, dir)
		return nil
	}}
	prepare.Flags().StringVar(&runtimeDir, "dir", "", "Runtime directory (default: configured runtime_dir)")
	runtimeCmd.AddCommand(prepare)
	root.AddCommand(runtimeCmd)
	docs := &cobra.Command{Use: "docs [query]", Short: "Search the bundled, versioned official Mermaid handbook", RunE: func(cmd *cobra.Command, args []string) error {
		query := strings.Join(args, " ")
		if query == "" {
			for _, p := range handbook.List() {
				fmt.Fprintf(cmd.OutOrStdout(), "%-24s %s\n", p.Slug, p.Title)
			}
			return nil
		}
		if p, body, e := handbook.Read(query); e == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "%s — Mermaid %s\n%s\n\n%s\n", p.Title, p.Version, p.Source, body)
			return nil
		}
		for _, h := range handbook.Search(query, 20) {
			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n  %s\n", h.Slug, h.Title, h.Excerpt)
		}
		return nil
	}}
	root.AddCommand(docs)
	cfgCmd := &cobra.Command{Use: "config", Short: "Inspect configuration"}
	var cfgJSON bool
	show := &cobra.Command{Use: "show", Short: "Show effective configuration", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		c, e := load(cmd)
		if e != nil {
			return e
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(c)
	}}
	show.Flags().BoolVar(&cfgJSON, "json", false, "Machine-readable output (also the default)")
	cfgCmd.AddCommand(show)
	root.AddCommand(cfgCmd)
	completion := &cobra.Command{Use: "completion [bash|zsh|fish|powershell]", Short: "Generate shell completion", Args: cobra.ExactArgs(1), ValidArgs: []string{"bash", "zsh", "fish", "powershell"}, RunE: func(cmd *cobra.Command, a []string) error {
		switch a[0] {
		case "bash":
			return root.GenBashCompletionV2(cmd.OutOrStdout(), true)
		case "zsh":
			return root.GenZshCompletion(cmd.OutOrStdout())
		case "fish":
			return root.GenFishCompletion(cmd.OutOrStdout(), true)
		case "powershell":
			return root.GenPowerShellCompletionWithDesc(cmd.OutOrStdout())
		default:
			return usage(fmt.Errorf("unsupported shell %q", a[0]))
		}
	}}
	root.AddCommand(completion)
	return root
}
func validFormat(s string) bool { return s == "svg" || s == "png" || s == "unicode" || s == "ascii" }
func readSource(cmd *cobra.Command, path string, line int) (string, error) {
	if path == "-" {
		b, e := io.ReadAll(cmd.InOrStdin())
		return string(b), e
	}
	b, e := os.ReadFile(path)
	if e != nil {
		return "", e
	}
	abs, e := filepath.Abs(path)
	if e != nil {
		return "", e
	}
	d, e := document.Parse(abs, b)
	if e != nil {
		return "", e
	}
	x, e := document.Select(d.Diagrams, line)
	return x.Source, e
}
func NewRenderer(c config.Config) *render.Service {
	return render.New(render.Options{Node: c.Render.Node, RuntimeDir: c.Render.RuntimeDir, Termaid: c.Render.Termaid, CacheDir: config.CacheDir(), Timeout: c.Timeout()})
}
