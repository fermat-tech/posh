/*
Command posh is a portable Unix-like command shell for Windows, written in Go
and similar in spirit to GNU bash.

Posh runs interactively with full line editing and persistent history, executes
shell scripts, and provides the core shell primitives: pipelines, redirections,
variable and command substitution, arithmetic and glob expansion, control flow,
shell functions, background jobs, and a set of built-in commands. Globbing,
tilde, and brace expansion are performed by the shell itself, so they work
identically regardless of the underlying Windows command processor.

The binary name is derived from the executable stem: rename the executable and
all prompts and diagnostics update to match.

# Usage

	posh                Start an interactive REPL.
	posh script.posh    Execute a script file.
	posh -c "command"   Run a single command string and exit.

On interactive startup, posh sources ~/.poshrc if it exists.

# Pipelines and lists

Commands are joined into pipelines with "|", and pipelines into lists with the
sequencing and short-circuit operators:

	ls | grep .go | wc -l
	make && echo ok
	test -f file || echo missing
	cd /tmp; ls; cd -

A trailing "&" runs a pipeline as a background job (see Jobs).

# Redirections

Posh supports the full set of file-descriptor redirections:

	cmd > out.txt        Truncate stdout to a file.
	cmd >> out.txt       Append stdout to a file.
	cmd < in.txt         Read stdin from a file.
	cmd 2> err.txt       Redirect stderr.
	cmd 2>> err.txt      Append stderr.
	cmd &> all.txt       Redirect stdout and stderr.
	cmd 2>&1             Duplicate stderr onto stdout.
	cmd N> file          Redirect arbitrary descriptor N.
	cmd N>&-             Close descriptor N.

# Here-documents and here-strings

A here-document feeds the following lines to a command's stdin until a line
matching the delimiter is reached:

	cat << EOF
	expanded: $HOME
	EOF

When the delimiter is quoted (<< 'EOF' or << "EOF") the body is literal and no
expansion is performed. The <<- form strips leading tab characters from the body
and the closing delimiter, so heredocs can be indented inside compound commands.
A here-string supplies a single expanded word as stdin, followed by a newline:

	cat <<< "$greeting"

# Expansion

Word expansion happens in the usual order: tilde, then parameter, command, and
arithmetic substitution, then word splitting and pathname (glob) expansion.

	echo Hello $NAME              Parameter expansion.
	echo "Hello ${NAME}"          Braced parameter expansion.
	echo $?                       Exit status of the last command.
	echo $$                       Shell process id.
	echo "Today is $(date)"       Command substitution.
	echo $((2 + 3 * 4))           Arithmetic expansion.
	echo *.go                     Pathname (glob) expansion.
	cd ~                          Tilde expansion.
	cp file.{txt,bak}             Brace expansion.
	printf '%s\n' $'a\tb'         ANSI-C quoting.

Single quotes preserve every character literally; double quotes allow parameter,
command, and arithmetic substitution while suppressing word splitting and
globbing; a backslash escapes the following character.

# Arrays

Posh supports indexed and associative arrays:

	arr=(one two three)          Indexed array literal.
	arr[1]=TWO                   Assign a single element (arithmetic subscript).
	arr+=(four)                  Append elements.
	echo "${arr[1]}"             Element by index (negative counts from the end).
	echo "${arr[@]}"             All elements (each a separate word when quoted).
	echo "${arr[*]}"             All elements joined into one word.
	echo "${#arr[@]}"            Number of elements.
	echo "${#arr[1]}"            Character length of one element.
	echo "${!arr[@]}"            The indices.

	declare -A m                 Declare an associative (string-keyed) array.
	m[admin]=rw                  Assign by key.
	declare -A m=([a]=1 [b]=2)   Inline initialization.
	echo "${m[admin]}"           Value for a key.
	echo "${!m[@]}"              The keys (sorted); "${m[@]}" gives the values.

A bare reference ($arr or ${arr}) yields element 0. `unset arr[i]` removes one
element; `unset arr` removes the whole variable.

# Control flow

Posh implements the standard compound commands:

	if COND; then LIST; elif COND; then LIST; else LIST; fi
	for NAME in WORDS; do LIST; done
	for NAME in WORDS; { LIST; }   bash brace-body form ({ } replace do/done)
	while COND; do LIST; done
	until COND; do LIST; done
	case WORD in PATTERN) LIST;; esac
	(( expression ))            Arithmetic command (status reflects the result).
	{ LIST; }                   Group commands in the current shell.
	( LIST )                    Run a list in a subshell.

# Functions

Functions are defined with either the POSIX or the keyword form and are invoked
like any other command (no parentheses at the call site). They are registered
when their definition is evaluated, so a function must be defined before it is
called.

	greet() { echo "Hello, $1"; }
	function greet { echo "Hello, $1"; }

	greet world

Inside a function, $1, $2, ... are the positional parameters and "return"
sets the exit status.

# Jobs

A pipeline ending in "&" runs in the background; "jobs" lists active jobs, and
"fg", "bg", "wait", and "kill" manage them.

	long-running-command &
	jobs
	fg %1

# Built-in commands

	cd        Change the working directory (default: $HOME).
	pwd       Print the working directory.
	echo      Print arguments.
	printf    Formatted output.
	export    Export a variable to child processes.
	unset     Remove a variable.
	env       Print the exported environment.
	set       Set shell variables and options (set -o).
	declare   Declare variables/arrays (declare -A / -a); alias typeset.
	source/.  Execute a file in the current shell context.
	alias     Define or list aliases.
	unalias   Remove aliases.
	history   Show command history.
	type      Report how a name would be resolved.
	help      Show built-in help.
	clear     Clear the screen.
	test/[    Evaluate a conditional expression.
	read      Read a line into variables.
	shift     Shift positional parameters.
	trap      Run a command on a signal.
	eval      Evaluate arguments as a command.
	mkdir     Create directories.
	jobs/fg/bg/wait/kill/ps   Job and process control.
	break/continue/return     Loop and function control flow.
	true/false/:              Status-only no-ops.
	exit      Exit the shell.

The file utilities ls, wc, which, grep, egrep, find, head, tail, and less are
provided as built-ins that delegate to the corresponding win* sibling tools when
they are present on PATH, falling back to any same-named tool otherwise.

# Command lookup

A bare command name is resolved as alias, then function, then built-in, then an
executable on PATH. On Windows, PATH lookup honors PATHEXT, so .EXE, .CMD, .BAT,
.PS1, and any other configured extension are found automatically. A name that
contains a path separator is run directly.

# Line editing

The interactive editor offers an emacs mode (the default, via the liner library)
and a vi mode selected with "set -o vi". History is saved to ~/.posh_history.
Common keys include Up/Down to navigate history, Ctrl+R for reverse search, Tab
to complete the word under the cursor, Ctrl+C to interrupt, and Ctrl+D to exit
on an empty line. In vi mode, ESC enters command mode for vi-style motions and
history navigation, and Tab still completes.

# Prompt

The prompt is taken from $PS1 (default "\u@\h \w \$ ") with the usual escapes:
\u (user), \h and \H (short and full hostname), \w and \W (working directory and
its basename), \$ ("#" for root, otherwise "$"), \n (newline), and \\ (a literal
backslash).

# Environment

On Windows, HOME is synthesized from USERPROFILE when it is not already set, so
"$HOME" and "~" work without configuration. Color in the prompt follows the
NO_COLOR convention; set NO_COLOR to any value to disable it.
*/
package main
