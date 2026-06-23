# posh

A portable Unix-like command shell for Windows, written in Go — similar in spirit to GNU bash.

`posh` runs interactively with full line-editing and persistent history, executes shell scripts, and provides the core shell primitives you expect: pipelines, redirections, variable expansion, command substitution, arithmetic expansion, glob expansion, background jobs, and a set of built-in commands.

The binary name is derived from the executable stem — rename it and all prompts update automatically.

---

## Installation

### Download binary (easiest)

Grab the latest pre-built binary from the [Releases](https://github.com/fermat-tech/posh/releases) page — no Go required.

| File | Platform |
|------|----------|
| `posh.exe` | Windows (amd64) |
| `posh-linux-amd64` | Linux (amd64) |

Place it somewhere on your `PATH` and you're done. On Linux, mark it executable first:

```sh
chmod +x posh-linux-amd64
mv posh-linux-amd64 ~/.local/bin/posh
```

### go install

Requires [Go](https://golang.org) 1.21+.

```powershell
go install github.com/fermat-tech/posh@latest
```

The binary lands in `%USERPROFILE%\go\bin` (Windows) or `~/go/bin` (Linux/macOS), which should already be on your `PATH`.

### Build from source

```powershell
git clone https://github.com/fermat-tech/posh.git
cd posh
go build -o posh.exe .   # Windows
go build -o posh .       # Linux / macOS
```

---

## Usage

```
posh                   Interactive REPL
posh script.posh       Execute a script file
posh -c "command"      Run a single command and exit
```

---

## Features (Phase 1)

### Pipelines
```sh
ls | wc -l
cat file.txt | grep foo | sort
```

### Redirections
```sh
echo hello > out.txt
cat >> log.txt
sort < input.txt
cmd 2> errors.txt
cmd &> all.txt        # stdout + stderr
```

### Variables
```sh
NAME=world
echo Hello $NAME
echo "Hello ${NAME}"
echo $?               # last exit code
echo $$               # current PID
```

### Command substitution
```sh
echo "Today is $(date)"
FILES=$(ls *.go)
```

### Arithmetic expansion
```sh
echo $((2 + 3 * 4))
SIZE=$((1024 * 1024))
```

### Glob expansion
```sh
echo *.go
ls src/**/*.txt
```

### Tilde expansion
```sh
cd ~
cat ~/notes.txt
```

### Logical operators and sequences
```sh
make && echo "Build OK"
test -f file || echo "Not found"
cd /tmp; ls; cd -
```

### Background jobs
```sh
long-running-command &
jobs
```

### Subshells
```sh
(cd /tmp && ls)
```

---

## Built-in commands

| Command | Description |
|---------|-------------|
| `cd [dir]` | Change directory (default: `$HOME`) |
| `pwd` | Print working directory |
| `echo [-n] [args]` | Print arguments |
| `printf fmt [args]` | Formatted output |
| `export [VAR[=val]]` | Export variable to child processes |
| `unset VAR...` | Remove variable |
| `env` | Print exported environment |
| `set [VAR=val]` | Set or list shell variables |
| `source FILE` / `. FILE` | Execute file in current shell context |
| `alias [name[=val]]` | Define or list aliases |
| `unalias name...` | Remove aliases |
| `history` | Show command history |
| `type name...` | Show whether name is a builtin, alias, or external |
| `jobs` | List background jobs |
| `true` / `false` / `:` | Exit 0 / exit 1 / no-op |
| `help` | Show built-in help |
| `exit [n]` | Exit with status `n` |

---

## Startup file

`posh` sources `~/.poshrc` on interactive startup.

```sh
# ~/.poshrc
alias ll='ls -la'
export EDITOR=notepad
PS1='\u@\h \w \$ '
```

---

## Prompt (PS1)

| Escape | Meaning |
|--------|---------|
| `\u` | Current username |
| `\h` | Short hostname |
| `\H` | Full hostname |
| `\w` | Working directory (`~` abbreviated) |
| `\W` | Basename of working directory |
| `\$` | `$` for normal users, `#` for root |
| `\n` | Newline |
| `\\` | Literal backslash |

---

## Line editing (interactive mode)

| Key | Action |
|-----|--------|
| Up / Down | Navigate history |
| Ctrl+R | Reverse history search |
| Ctrl+C | Interrupt (return to prompt) |
| Ctrl+D | Exit on empty line |
| Tab | Filename completion |

History is saved to `~/.posh_history` (max 1000 entries).

---

## Command lookup

On Windows, `posh` respects `PATHEXT` when looking up commands — it will find `.EXE`, `.CMD`, `.BAT`, `.PS1`, and any other extension listed in `PATHEXT`.

---

## Color

Color in the prompt follows `NO_COLOR` (see [no-color.org](https://no-color.org)). Set `NO_COLOR=1` to disable.

---

## License

[MIT](https://opensource.org/licenses/MIT)
