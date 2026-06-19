package eval

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/fermat-tech/posh/internal/lexer"
	"github.com/fermat-tech/posh/internal/parser"
)

// applyRedirs opens/creates files for all redirections in cmd and wires them
// to the appropriate file descriptors of the exec.Cmd.
// It returns a cleanup function that closes any files it opened.
func applyRedirs(redirs []parser.Redir, stdin io.Reader, stdout, stderr io.Writer) (
	io.Reader, io.Writer, io.Writer, func(), error,
) {
	var closers []io.Closer
	cleanup := func() {
		for _, c := range closers {
			c.Close()
		}
	}

	for _, r := range redirs {
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

		case lexer.HEREDOC_OP:
			stdin = strings.NewReader(r.File)
		}
	}

	return stdin, stdout, stderr, cleanup, nil
}
