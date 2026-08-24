package main

import (
	"flag"
	"fmt"
	"strings"
)

// The CLI is stdlib `flag` with a hand-rolled dispatch (AGENTS.md: no CLI
// framework dependency), so there is no generator to lean on. These scripts are
// static and complete only the two things worth completing — the subcommand and
// the flag names — which is all a framework would have given us anyway.
//
// commands must stay in sync with the dispatch switch in main().
var commands = []string{
	"quickstart", "doctor", "init", "goal", "amend", "run", "status", "events",
	"bench", "serve", "eval", "diagnose", "otel", "approve", "reject",
	"version", "completion", "help",
}

// commonFlags are shared by nearly every subcommand; per-command extras are
// deliberately omitted rather than listed wrongly.
var commonFlags = []string{"--path", "--json", "--help"}

func cmdCompletion(args []string) error {
	fs := flag.NewFlagSet("completion", flag.ExitOnError)
	describe(fs,
		"aoa completion — print a shell completion script.\n\n"+
			"Add it to your shell's startup file, or source it directly.",
		"aoa completion zsh > \"${fpath[1]}/_aoa\"")
	if err := fs.Parse(args); err != nil {
		return err
	}
	shell := fs.Arg(0)
	cmds := strings.Join(commands, " ")
	flags := strings.Join(commonFlags, " ")

	switch shell {
	case "bash":
		fmt.Printf(`# aoa bash completion — add to ~/.bashrc:
#   source <(aoa completion bash)
_aoa() {
    local cur prev
    cur="${COMP_WORDS[COMP_CWORD]}"
    if [ "$COMP_CWORD" -eq 1 ]; then
        COMPREPLY=( $(compgen -W "%s" -- "$cur") )
        return
    fi
    case "$cur" in
        -*) COMPREPLY=( $(compgen -W "%s" -- "$cur") ) ;;
        *)  COMPREPLY=( $(compgen -f -- "$cur") ) ;;
    esac
}
complete -F _aoa aoa
`, cmds, flags)
	case "zsh":
		fmt.Printf(`#compdef aoa
# aoa zsh completion — install with:
#   aoa completion zsh > "${fpath[1]}/_aoa" && compinit
_aoa() {
    local -a cmds
    cmds=(%s)
    if (( CURRENT == 2 )); then
        _describe 'command' cmds
        return
    fi
    _arguments '*:file:_files' %s
}
_aoa "$@"
`, cmds, zshFlagSpecs())
	case "fish":
		fmt.Printf("# aoa fish completion — install with:\n#   aoa completion fish > ~/.config/fish/completions/aoa.fish\n")
		for _, c := range commands {
			fmt.Printf("complete -c aoa -n __fish_use_subcommand -a %s\n", c)
		}
		for _, f := range commonFlags {
			fmt.Printf("complete -c aoa -l %s\n", strings.TrimPrefix(f, "--"))
		}
	default:
		return fmt.Errorf("usage: aoa completion bash|zsh|fish")
	}
	return nil
}

func zshFlagSpecs() string {
	var b strings.Builder
	for _, f := range commonFlags {
		fmt.Fprintf(&b, "'%s' ", f)
	}
	return strings.TrimSpace(b.String())
}
