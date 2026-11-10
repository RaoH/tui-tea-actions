package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"gopkg.in/yaml.v3"
)

type config struct {
	URL      string
	Token    string
	Login    string
	PerRepo  int
	Interval time.Duration
	Owner    string
	Repos    []string
	Notify   string
	Status   string
	Auto     string
	User     string
}

type teaLogin struct {
	Name    string `yaml:"name"`
	URL     string `yaml:"url"`
	Token   string `yaml:"token"`
	Default bool   `yaml:"default"`
}

// teaCredentials reads URL and token from the tea CLI config, so a working
// `tea login` is enough to run this tool.
func teaCredentials(name string) (url, token string, err error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", "", err
	}
	paths := []string{filepath.Join(dir, "tea", "config.yml")}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".config", "tea", "config.yml"))
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var cfg struct {
			Logins []teaLogin `yaml:"logins"`
		}
		if err := yaml.Unmarshal(b, &cfg); err != nil {
			return "", "", fmt.Errorf("parse %s: %w", p, err)
		}
		var pick *teaLogin
		for i, l := range cfg.Logins {
			if (name != "" && l.Name == name) || (name == "" && l.Default) {
				pick = &cfg.Logins[i]
				break
			}
		}
		if pick == nil && name == "" && len(cfg.Logins) > 0 {
			pick = &cfg.Logins[0]
		}
		if pick != nil {
			return pick.URL, pick.Token, nil
		}
		if name != "" {
			return "", "", fmt.Errorf("tea login %q not found in %s", name, p)
		}
	}
	return "", "", errors.New("no tea config found")
}

func loadConfig() (config, error) {
	var c config
	var repos string
	flag.StringVar(&c.URL, "url", os.Getenv("FORGEJO_URL"), "Forgejo base URL (env FORGEJO_URL)")
	flag.StringVar(&c.Token, "token", os.Getenv("FORGEJO_TOKEN"), "API token (env FORGEJO_TOKEN)")
	flag.StringVar(&c.Login, "login", "", "tea login name to take URL/token from (default: tea's default login)")
	flag.IntVar(&c.PerRepo, "n", 10, "number of recent runs to fetch per repo")
	flag.DurationVar(&c.Interval, "interval", 15*time.Second, "auto-refresh interval (0 disables)")
	flag.StringVar(&c.Owner, "owner", "", "only show repos owned by this user/org")
	flag.StringVar(&repos, "repos", "", "comma-separated owner/repo list (skips repo discovery)")
	flag.StringVar(&c.Notify, "notify", notifyOSC777, "how to signal a watched run finishing: osc777, osc9, bell, off")
	flag.StringVar(&c.Status, "status", "auto", "report watch state on the terminal tab via OSC 7501: auto (in Rex), on, off")
	flag.StringVar(&c.Auto, "autowatch", "off", "watch new runs automatically: off, mine (triggered by you), all")
	flag.StringVar(&c.User, "user", "", "login that counts as \"mine\" for -autowatch (default: the token's user)")
	flag.Parse()

	if _, ok := parseAutoMode(c.Auto); !ok {
		return c, fmt.Errorf("invalid -autowatch %q: use off, mine or all", c.Auto)
	}

	if c.Status != "auto" && c.Status != "on" && c.Status != "off" {
		return c, fmt.Errorf("invalid -status %q: use auto, on or off", c.Status)
	}
	if !validNotifyMode(c.Notify) {
		return c, fmt.Errorf("invalid -notify %q: use osc777, osc9, bell or off", c.Notify)
	}

	if repos != "" {
		for _, r := range strings.Split(repos, ",") {
			if r = strings.TrimSpace(r); r != "" {
				c.Repos = append(c.Repos, r)
			}
		}
	}
	if c.URL == "" || c.Token == "" {
		u, t, err := teaCredentials(c.Login)
		if err != nil && c.URL == "" {
			return c, fmt.Errorf("no Forgejo URL: pass -url, set FORGEJO_URL, or run `tea login add` (%v)", err)
		}
		if c.URL == "" {
			c.URL = u
		}
		if c.Token == "" && strings.TrimRight(u, "/") == strings.TrimRight(c.URL, "/") {
			c.Token = t
		}
	}
	return c, nil
}

func discoverRepos(c config, client *Client) ([]string, error) {
	if len(c.Repos) > 0 {
		return c.Repos, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	repos, err := client.Repos(ctx)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, r := range repos {
		if c.Owner == "" || strings.EqualFold(strings.SplitN(r.FullName, "/", 2)[0], c.Owner) {
			names = append(names, r.FullName)
		}
	}
	return names, nil
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	client := NewClient(cfg.URL, cfg.Token)
	p := tea.NewProgram(newModel(cfg, client), tea.WithAltScreen())
	_, err = p.Run()
	if statusSupported(cfg.Status) {
		sendProgramStatus(statusClear, "")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
