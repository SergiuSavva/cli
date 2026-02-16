package comment

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/pkg/cmd/pr/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type InlineOptions struct {
	Path string
	Line string
	Side string
}

func NewCmdComment(f *cmdutil.Factory, runF func(*shared.CommentableOptions) error) *cobra.Command {
	opts := &shared.CommentableOptions{
		IO:                        f.IOStreams,
		HttpClient:                f.HttpClient,
		EditSurvey:                shared.CommentableEditSurvey(f.Config, f.IOStreams),
		InteractiveEditSurvey:     shared.CommentableInteractiveEditSurvey(f.Config, f.IOStreams),
		ConfirmSubmitSurvey:       shared.CommentableConfirmSubmitSurvey(f.Prompter),
		ConfirmCreateIfNoneSurvey: shared.CommentableInteractiveCreateIfNoneSurvey(f.Prompter),
		ConfirmDeleteLastComment:  shared.CommentableConfirmDeleteLastComment(f.Prompter),
		OpenInBrowser:             f.Browser.Browse,
	}

	var bodyFile string
	var inlineOpts InlineOptions

	cmd := &cobra.Command{
		Use:   "comment [<number> | <url> | <branch>]",
		Short: "Add a comment to a pull request",
		Long: heredoc.Doc(`
			Add a comment to a GitHub pull request.

			Without the body text supplied through flags, the command will interactively
			prompt for the comment text.

			Use --path and --line to add an inline comment on a specific line in the diff.
			Inline comments are added as review comments on the pull request.

			Use --side to specify which side of the diff the line number refers to:
			RIGHT (default) for added or unchanged lines, LEFT for deleted lines.
		`),
		Example: heredoc.Doc(`
			# Add a general comment to a pull request
			$ gh pr comment 13 --body "Hi from GitHub CLI"

			# Add an inline comment on a specific line (defaults to RIGHT side)
			$ gh pr comment 13 --body "Consider using optional chaining" --path "src/utils.ts" --line 42

			# Add a multi-line inline comment
			$ gh pr comment 13 --body "This block needs refactoring" --path "src/api.ts" --line 10-20

			# Add an inline comment on a deleted line (LEFT side of the diff)
			$ gh pr comment 13 --body "Why was this removed?" --path "src/api.ts" --line 15 --side LEFT
		`),
		Args: cobra.MaximumNArgs(1),
		PreRunE: func(cmd *cobra.Command, args []string) error {
			if repoOverride, _ := cmd.Flags().GetString("repo"); repoOverride != "" && len(args) == 0 {
				return cmdutil.FlagErrorf("argument required when using the --repo flag")
			}

			// Validate inline comment flags
			if err := validateInlineFlags(cmd, &inlineOpts, opts); err != nil {
				return err
			}

			// If inline comment, skip the standard CommentablePreRun validation
			if inlineOpts.Path != "" {
				return nil
			}

			var selector string
			if len(args) > 0 {
				selector = args[0]
			}
			fields := []string{"id", "url"}
			if opts.EditLast || opts.DeleteLast {
				fields = append(fields, "comments")
			}
			finder := shared.NewFinder(f)
			opts.RetrieveCommentable = func() (shared.Commentable, ghrepo.Interface, error) {
				return finder.Find(shared.FindOptions{
					Selector: selector,
					Fields:   fields,
				})
			}
			return shared.CommentablePreRun(cmd, opts)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if bodyFile != "" {
				b, err := cmdutil.ReadFile(bodyFile, opts.IO.In)
				if err != nil {
					return err
				}
				opts.Body = string(b)
			}

			// Handle inline comment
			if inlineOpts.Path != "" {
				var selector string
				if len(args) > 0 {
					selector = args[0]
				}
				return runInlineComment(f, opts.IO, selector, opts.Body, &inlineOpts)
			}

			if runF != nil {
				return runF(opts)
			}
			return shared.CommentableRun(opts)
		},
	}

	cmd.Flags().StringVarP(&opts.Body, "body", "b", "", "The comment body `text`")
	cmd.Flags().StringVarP(&bodyFile, "body-file", "F", "", "Read body text from `file` (use \"-\" to read from standard input)")
	cmd.Flags().BoolP("editor", "e", false, "Skip prompts and open the text editor to write the body in")
	cmd.Flags().BoolP("web", "w", false, "Open the web browser to write the comment")
	cmd.Flags().BoolVar(&opts.EditLast, "edit-last", false, "Edit the last comment of the current user")
	cmd.Flags().BoolVar(&opts.DeleteLast, "delete-last", false, "Delete the last comment of the current user")
	cmd.Flags().BoolVar(&opts.DeleteLastConfirmed, "yes", false, "Skip the delete confirmation prompt when --delete-last is provided")
	cmd.Flags().BoolVar(&opts.CreateIfNone, "create-if-none", false, "Create a new comment if no comments are found. Can be used only with --edit-last")

	// Inline comment flags
	cmd.Flags().StringVarP(&inlineOpts.Path, "path", "p", "", "File path in the diff to comment on (for inline comments)")
	cmd.Flags().StringVarP(&inlineOpts.Line, "line", "L", "", "Line number or range (e.g., 42 or 10-20) to comment on")
	cmd.Flags().StringVarP(&inlineOpts.Side, "side", "s", "RIGHT", "Side of the diff to comment on: LEFT (deleted lines) or RIGHT (added/unchanged lines)")

	return cmd
}

func validateInlineFlags(cmd *cobra.Command, inlineOpts *InlineOptions, opts *shared.CommentableOptions) error {
	pathProvided := cmd.Flags().Changed("path")
	lineProvided := cmd.Flags().Changed("line")
	sideProvided := cmd.Flags().Changed("side")

	// If no inline flags, nothing to validate here
	if !pathProvided && !lineProvided && !sideProvided {
		return nil
	}

	// --path requires --line
	if pathProvided && !lineProvided {
		return cmdutil.FlagErrorf("--line is required when using --path")
	}

	// --line requires --path
	if lineProvided && !pathProvided {
		return cmdutil.FlagErrorf("--path is required when using --line")
	}

	// --side requires --path
	if sideProvided && !pathProvided {
		return cmdutil.FlagErrorf("--path is required when using --side")
	}

	// Validate --side value
	if sideProvided {
		side := strings.ToUpper(inlineOpts.Side)
		if side != "LEFT" && side != "RIGHT" {
			return cmdutil.FlagErrorf("--side must be LEFT or RIGHT")
		}
		inlineOpts.Side = side
	}

	// Inline comments require body
	bodyProvided := cmd.Flags().Changed("body") || cmd.Flags().Changed("body-file")
	if pathProvided && !bodyProvided {
		return cmdutil.FlagErrorf("--body or --body-file is required for inline comments")
	}

	// Inline comments are mutually exclusive with other modes
	if pathProvided {
		web, _ := cmd.Flags().GetBool("web")
		editor, _ := cmd.Flags().GetBool("editor")

		if web {
			return cmdutil.FlagErrorf("--path cannot be used with --web")
		}
		if editor {
			return cmdutil.FlagErrorf("--path cannot be used with --editor")
		}
		if opts.EditLast {
			return cmdutil.FlagErrorf("--path cannot be used with --edit-last")
		}
		if opts.DeleteLast {
			return cmdutil.FlagErrorf("--path cannot be used with --delete-last")
		}
	}

	// Validate line format
	if lineProvided {
		if _, _, err := parseLineRange(inlineOpts.Line); err != nil {
			return cmdutil.FlagErrorf("invalid --line value: %s", err)
		}
	}

	return nil
}

// parseLineRange parses a line string like "42" or "10-20" into start and end line numbers.
// For single lines, start and end are the same.
func parseLineRange(line string) (start, end int, err error) {
	if strings.Contains(line, "-") {
		parts := strings.SplitN(line, "-", 2)
		start, err = strconv.Atoi(parts[0])
		if err != nil {
			return 0, 0, fmt.Errorf("invalid start line: %s", parts[0])
		}
		end, err = strconv.Atoi(parts[1])
		if err != nil {
			return 0, 0, fmt.Errorf("invalid end line: %s", parts[1])
		}
		if start > end {
			return 0, 0, fmt.Errorf("start line %d cannot be greater than end line %d", start, end)
		}
		if start < 1 || end < 1 {
			return 0, 0, fmt.Errorf("line numbers must be positive")
		}
	} else {
		start, err = strconv.Atoi(line)
		if err != nil {
			return 0, 0, fmt.Errorf("invalid line number: %s", line)
		}
		if start < 1 {
			return 0, 0, fmt.Errorf("line numbers must be positive")
		}
		end = start
	}
	return start, end, nil
}

func runInlineComment(f *cmdutil.Factory, io *iostreams.IOStreams, selector string, body string, inlineOpts *InlineOptions) error {
	httpClient, err := f.HttpClient()
	if err != nil {
		return err
	}

	finder := shared.NewFinder(f)
	pr, repo, err := finder.Find(shared.FindOptions{
		Selector: selector,
		Fields:   []string{"number", "headRefOid"},
	})
	if err != nil {
		return err
	}

	startLine, endLine, err := parseLineRange(inlineOpts.Line)
	if err != nil {
		return err
	}

	side := inlineOpts.Side
	if side == "" {
		side = "RIGHT"
	}

	input := api.PRReviewCommentInput{
		Body:     body,
		CommitID: pr.HeadRefOid,
		Path:     inlineOpts.Path,
		Line:     endLine,
		Side:     side,
	}

	// For multi-line comments, set start line
	if startLine != endLine {
		input.StartLine = startLine
		input.StartSide = side
	}

	apiClient := api.NewClientFromHTTP(httpClient)
	commentURL, err := api.CreatePRReviewComment(apiClient, repo, pr.Number, input)
	if err != nil {
		var httpErr api.HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == 422 {
			return fmt.Errorf("failed to create inline comment: %w\nHint: ensure the file path and line number exist in the pull request diff", err)
		}
		return fmt.Errorf("failed to create inline comment: %w", err)
	}

	fmt.Fprintln(io.Out, commentURL)
	return nil
}
