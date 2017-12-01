// Partially derived from github.com/rsc/github.

package main

import (
	"context"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/go-github/github"
	"golang.org/x/oauth2"
)

var (
	project      = flag.String("p", "cockroachdb/cockroach", "GitHub owner/repo name")
	tokenFile    = flag.String("token", "", "read GitHub token personal access token from `file` (default $HOME/.github-issue-token)")
	projectOwner = ""
	projectRepo  = ""
)

func usage() {
	fmt.Fprintf(os.Stderr, `usage: re [-p owner/repo] pr

If query is a single number, prints the full history for the issue.
Otherwise, prints a table of matching results.
`)
	flag.PrintDefaults()
	os.Exit(2)
}

func main() {
	flag.Usage = usage
	flag.Parse()
	if flag.NArg() == 0 {
		usage()
	}

	q := strings.Join(flag.Args(), " ")
	switch q {
	case "out":
	case "in":
	}

	f := strings.Split(*project, "/")
	if len(f) != 2 {
		log.Fatal("invalid form for -p argument: must be owner/repo, like golang/go")
	}
	projectOwner = f[0]
	projectRepo = f[1]

	loadAuth()

	ctx := context.Background()

	n, _ := strconv.Atoi(q)
	if n != 0 {
		cmd := exec.Command("git", "fetch", "https://github.com/cockroachdb/cockroach", fmt.Sprintf("refs/pull/%d/head:reviews/pr/%d", n, n))

		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			log.Fatal(fmt.Errorf("invoking fetch: %v", err))
		}

		pr, _, err := client.PullRequests.Get(ctx, projectOwner, projectRepo, n)
		if err != nil {
			log.Fatal(err)
		}

		base := *pr.Base.SHA
		head := *pr.Head.SHA

		pretty := `--pretty=tformat:%C(yellow)commit %H%nAuthor: %an <%ae>%nDate:   %ad%n%n%w(0,4,4)%B`
		cmd = exec.Command("git", "show", "--reverse", pretty, fmt.Sprintf("%s..%s", base, head))
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			log.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			log.Fatal(err)
		}
		showOutput, err := ioutil.ReadAll(stdout)
		if err != nil {
			log.Fatal(err)
		}
		if err := cmd.Wait(); err != nil {
			log.Fatal(err)
		}

		updated := editText(showOutput)

		comments, err := parseFile(updated)
		if err != nil {
			log.Fatal(err)
		}

		postComments(ctx, n, comments)
	}
}

func editText(original []byte) []byte {
	f, err := ioutil.TempFile("", "re-edit-")
	if err != nil {
		log.Fatal(err)
	}
	if err := ioutil.WriteFile(f.Name(), original, 0666); err != nil {
		log.Fatal(err)
	}
	if err := runEditor(f.Name()); err != nil {
		log.Fatal(err)
	}
	updated, err := ioutil.ReadFile(f.Name())
	if err != nil {
		log.Fatal(err)
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return updated
}

func runEditor(filename string) error {
	ed := os.Getenv("VISUAL")
	if ed == "" {
		ed = os.Getenv("EDITOR")
	}
	if ed == "" {
		ed = "ed"
	}

	// If the editor contains spaces or other magic shell chars,
	// invoke it as a shell command. This lets people have
	// environment variables like "EDITOR=emacs -nw".
	// The magic list of characters and the idea of running
	// sh -c this way is taken from git/run-command.c.
	var cmd *exec.Cmd
	if strings.ContainsAny(ed, "|&;<>()$`\\\"' \t\n*?[#~=%") {
		cmd = exec.Command("sh", "-c", ed+` "$@"`, "$EDITOR", filename)
	} else {
		cmd = exec.Command(ed, filename)
	}

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("invoking editor: %v", err)
	}
	return nil
}

var client *github.Client

// GitHub personal access token, from https://github.com/settings/applications.
var authToken string

func loadAuth() {
	const short = ".github-issue-token"
	filename := filepath.Clean(os.Getenv("HOME") + "/" + short)
	shortFilename := filepath.Clean("$HOME/" + short)
	if *tokenFile != "" {
		filename = *tokenFile
		shortFilename = *tokenFile
	}
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		log.Fatal("reading token: ", err, "\n\n"+
			"Please create a personal access token at https://github.com/settings/tokens/new\n"+
			"and write it to ", shortFilename, " to use this program.\n"+
			"The token only needs the repo scope, or private_repo if you want to\n"+
			"view or edit issues for private repositories.\n"+
			"The benefit of using a personal access token over using your GitHub\n"+
			"password directly is that you can limit its use and revoke it at any time.\n\n")
	}
	fi, err := os.Stat(filename)
	if fi.Mode()&0077 != 0 {
		log.Fatalf("reading token: %s mode is %#o, want %#o", shortFilename, fi.Mode()&0777, fi.Mode()&0700)
	}
	authToken = strings.TrimSpace(string(data))
	t := &oauth2.Transport{
		Source: &tokenSource{AccessToken: authToken},
	}
	client = github.NewClient(&http.Client{Transport: t})
}

type tokenSource oauth2.Token

func (t *tokenSource) Token() (*oauth2.Token, error) {
	return (*oauth2.Token)(t), nil
}

var commitStart = regexp.MustCompile(`^commit (.*)$`)
var diffStart = `diff --git `
var fileStart = regexp.MustCompile(`^\+\+\+ b\/(.*)$`)
var hunkStart = `@@`

type commitMap = map[string]fileMap
type fileMap = map[string]commentMap
type commentMap = map[int]string

func parseFile(b []byte) (commitMap, error) {
	dat := string(b)

	commit := ""
	file := ""
	num := 0
	foundFirstHunk := false

	comments := make(commitMap)
	commentStart := -1
	lastCommentStart := -1

	off := 0
	for _, line := range strings.SplitAfter(dat, "\n") {
		if line == "" {
			break
		}
		off += len(line)
		line = strings.TrimRight(line, "\n")
		lastCommentStart = commentStart
		commentStart = -1

		commitMatches := commitStart.FindStringSubmatch(line)
		if len(commitMatches) > 1 {
			foundFirstHunk = false
			commit = commitMatches[1]
			continue
		}

		if strings.HasPrefix(line, diffStart) {
			foundFirstHunk = false
			continue
		}

		fileMatches := fileStart.FindStringSubmatch(line)
		if len(fileMatches) > 1 {
			file = fileMatches[1]
			continue
		}
		if !foundFirstHunk {
			if strings.HasPrefix(line, hunkStart) {
				foundFirstHunk = true
				num = 1
			}
			continue
		}

		if len(line) > 0 {
			switch line[0] {
			case '+', '-', ' ', '@':
				num++
				continue
			case '*':
				// Old comment
				continue
			}
		}
		// We found a comment!
		commentStart = lastCommentStart
		if commentStart == -1 {
			commentStart = off - len(line) - 1
		}
		if comments[commit] == nil {
			comments[commit] = make(fileMap)
		}
		if comments[commit][file] == nil {
			comments[commit][file] = make(commentMap)
		}
		comments[commit][file][num] = dat[commentStart:off]
	}

	return comments, nil
}

func postComments(ctx context.Context, pr int, c commitMap) error {
	review := &github.PullRequestReviewRequest{}
	for _, fileMap := range c {
		for f, commentMap := range fileMap {
			file := f
			for o := range commentMap {
				offset := o
				comment := commentMap[o]
				req := &github.DraftReviewComment{
					Path:     &file,
					Position: &offset,
					Body:     &comment,
				}
				review.Comments = append(review.Comments, req)
			}
		}
	}
	_, _, err := client.PullRequests.CreateReview(ctx, projectOwner, projectRepo, pr, review)
	if err != nil {
		return err
	}
	return nil
}
