package eval

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/fermat-tech/posh/internal/lexer"
	"github.com/fermat-tech/posh/internal/parser"
)

// writerForFd returns the current writer for fd 1 (stdout) or 2 (stderr).
func writerForFd(fd int, stdout, stderr io.Writer) io.Writer {
	switch fd {
	case 1:
		return stdout
	case 2:
		return stderr
	}
	return nil
}

// applyRedirs opens/creates files for all redirections in cmd and wires them
// to the appropriate file descriptors of the exec.Cmd.
// It returns a cleanup function that closes any files it opened.
func applyRedirs(sh *Shell, redirs []parser.Redir, stdin io.Reader, stdout, stderr io.Writer) (
	io.Reader, io.Writer, io.Writer, func(), error,
) {
	var closers []io.Closer
	cleanup := func() {
		for _, c := range closers {
			c.Close()
		}
	}

	for _, r := range redirs {
		// Expand the target of filename-based redirections — parameter, command,
		// arithmetic, and tilde expansion plus quote removal — so `> "$dir/f"`,
		// `> ~/f`, and quoted paths resolve. r is a loop copy, so mutating it is
		// local to this iteration. Heredoc/here-string ops carry body content in
		// r.File and are expanded separately below.
		switch r.Op {
		case lexer.REDIR_OUT, lexer.REDIR_APPEND, lexer.REDIR_IN,
			lexer.REDIR_ERR, lexer.REDIR_ERR_APPEND, lexer.REDIR_BOTH,
			lexer.REDIR_BOTH_APPEND, lexer.REDIR_FD_OUT, lexer.REDIR_FD_APPEND,
			lexer.REDIR_FD_IN:
			r.File = unprotectWord(sh.expandWord(r.File))
		}

		switch r.Op {
		case lexer.REDIR_OUT:
			f, err := os.Create(r.File)
			if err != nil {
				cleanup()
				return nil, nil, nil, nil, fmt.Errorf("cannot open %q: %w", r.File, err)
			}
			closers = append(closers, f)
			stdout = f

		case lexer.REDIR_APPEND:
			f, err := os.OpenFile(r.File, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0666)
			if err != nil {
				cleanup()
				return nil, nil, nil, nil, fmt.Errorf("cannot open %q: %w", r.File, err)
			}
			closers = append(closers, f)
			stdout = f

		case lexer.REDIR_IN:
			f, err := os.Open(r.File)
			if err != nil {
				cleanup()
				return nil, nil, nil, nil, fmt.Errorf("cannot open %q: %w", r.File, err)
			}
			closers = append(closers, f)
			stdin = f

		case lexer.REDIR_ERR:
			f, err := os.Create(r.File)
			if err != nil {
				cleanup()
				return nil, nil, nil, nil, fmt.Errorf("cannot open %q: %w", r.File, err)
			}
			closers = append(closers, f)
			stderr = f

		case lexer.REDIR_ERR_APPEND:
			f, err := os.OpenFile(r.File, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0666)
			if err != nil {
				cleanup()
				return nil, nil, nil, nil, fmt.Errorf("cannot open %q: %w", r.File, err)
			}
			closers = append(closers, f)
			stderr = f

		case lexer.REDIR_BOTH:
			f, err := os.Create(r.File)
			if err != nil {
				cleanup()
				return nil, nil, nil, nil, fmt.Errorf("cannot open %q: %w", r.File, err)
			}
			closers = append(closers, f)
			stdout = f
			stderr = f

		case lexer.REDIR_BOTH_APPEND:
			f, err := os.OpenFile(r.File, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0666)
			if err != nil {
				cleanup()
				return nil, nil, nil, nil, fmt.Errorf("cannot open %q: %w", r.File, err)
			}
			closers = append(closers, f)
			stdout = f
			stderr = f

		case lexer.REDIR_FD_OUT:
			f, err := os.Create(r.File)
			if err != nil {
				cleanup()
				return nil, nil, nil, nil, fmt.Errorf("cannot open %q: %w", r.File, err)
			}
			closers = append(closers, f)
			switch r.Fd1 {
			case 1:
				stdout = f
			case 2:
				stderr = f
			}

		case lexer.REDIR_FD_APPEND:
			f, err := os.OpenFile(r.File, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0666)
			if err != nil {
				cleanup()
				return nil, nil, nil, nil, fmt.Errorf("cannot open %q: %w", r.File, err)
			}
			closers = append(closers, f)
			switch r.Fd1 {
			case 1:
				stdout = f
			case 2:
				stderr = f
			}

		case lexer.REDIR_FD_IN:
			f, err := os.Open(r.File)
			if err != nil {
				cleanup()
				return nil, nil, nil, nil, fmt.Errorf("cannot open %q: %w", r.File, err)
			}
			closers = append(closers, f)
			if r.Fd1 == 0 {
				stdin = f
			}

		case lexer.REDIR_DUP_OUT:
			// N>&M — make fd N write to the same place as fd M.
			dst := writerForFd(r.Fd2, stdout, stderr)
			if dst != nil {
				switch r.Fd1 {
				case 1:
					stdout = dst
				case 2:
					stderr = dst
				}
			}

		case lexer.REDIR_DUP_IN:
			// N<&M — make fd N read from the same place as fd M.
			if r.Fd2 == 0 {
				switch r.Fd1 {
				case 0:
					// 0<&0 — no-op
				}
			}

		case lexer.REDIR_CLOSE_OUT:
			switch r.Fd1 {
			case 1:
				stdout = io.Discard
			case 2:
				stderr = io.Discard
			}

		case lexer.REDIR_CLOSE_IN:
			if r.Fd1 == 0 {
				stdin = strings.NewReader("")
			}

		case lexer.HEREDOC_OP:
			body := r.File
			if r.Expand && sh != nil {
				body = sh.expandHeredocBody(body)
			}
			stdin = strings.NewReader(body)

		case lexer.HERESTRING_OP:
			word := r.File
			if sh != nil {
				word = sh.expandWord(word)
				word = unprotectWord(word)
			}
			stdin = strings.NewReader(word + "\n")
		}
	}

	return stdin, stdout, stderr, cleanup, nil
}
