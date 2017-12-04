// Partially derived from github.com/rsc/github.

package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/github"
	"golang.org/x/oauth2"
)

var (
	project      = flag.String("p", "cockroachdb/cockroach", "GitHub owner/repo name")
	resume       = flag.String("resume", "", "resume review from `file`")
	tokenFile    = flag.String("token", "", "read GitHub token personal access token from `file` (default $HOME/.github-issue-token)")
	projectOwner = ""
	projectRepo  = ""
)

func usage() {
	fmt.Fprintf(os.Stderr, `usage: re [-p owner/repo] [-resume file] pr-number

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
	// TODO(jordan) list prs with this
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
		var filename string
		if *resume != "" {
			filename = *resume
		} else {
			cmd := exec.Command("git", "fetch", "https://github.com/cockroachdb/cockroach", fmt.Sprintf("refs/pull/%d/head:reviews/pr/%d", n, n))
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			log.Printf("Fetching refs for PR %d", n)
			if err := cmd.Run(); err != nil {
				log.Fatal(fmt.Errorf("invoking fetch: %v", err))
			}

			log.Printf("Fetching details for PR %d", n)
			pr, _, err := client.PullRequests.Get(ctx, projectOwner, projectRepo, n)
			if err != nil {
				log.Fatal(err)
			}

			buf := bytes.NewBuffer(make([]byte, 0, 1024))
			printPR(ctx, buf, pr)

			pretty := `--pretty=tformat:commit %H%nAuthor: %an <%ae>%nDate:   %ad%n%n%w(0,4,4)%B`
			cmd = exec.Command("git", "show", "--reverse", pretty, fmt.Sprintf("%s..%s", *pr.Base.SHA, *pr.Head.SHA))
			if err := readPipe(cmd, buf); err != nil {
				log.Fatal(err)
			}

			f, err := ioutil.TempFile("", "re-edit-")
			if err != nil {
				log.Fatal(err)
			}
			if err := ioutil.WriteFile(f.Name(), buf.Bytes(), 0666); err != nil {
				log.Fatal(err)
			}
			filename = f.Name()
			f.Close()
		}

		request := review(n, filename)
		postComments(ctx, n, request)
	}
}

func postComments(ctx context.Context, pr int, review *github.PullRequestReviewRequest) error {
	fmt.Printf("Submitting review... ")
	_, _, err := client.PullRequests.CreateReview(ctx, projectOwner, projectRepo, pr, review)
	if err != nil {
		return err
	}
	fmt.Printf("success.\n")
	return nil
}

func exitHappy(args ...interface{}) {
	if len(args) > 0 {
		fmt.Println(args...)
	}
	os.Exit(0)
}

func readPipe(cmd *exec.Cmd, buf *bytes.Buffer) error {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	if _, err := buf.ReadFrom(stdout); err != nil {
		return err
	}
	if err := cmd.Wait(); err != nil {
		return err
	}
	return nil
}

func editFile(filename string) ([]byte, error) {
	if err := runEditor(filename); err != nil {
		return nil, err
	}
	updated, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	return updated, nil
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

func wrap(t string, prefix string) string {
	out := ""
	t = strings.Replace(t, "\r\n", "\n", -1)
	lines := strings.Split(t, "\n")
	for i, line := range lines {
		if i > 0 {
			out += "\n" + prefix
		}
		s := line
		for len(s) > 70 {
			i := strings.LastIndex(s[:70], " ")
			if i < 0 {
				i = 69
			}
			i++
			out += s[:i] + "\n" + prefix
			s = s[i:]
		}
		out += s
	}
	return out
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

func getInt(x *int) int {
	if x == nil {
		return 0
	}
	return *x
}

func getString(x *string) string {
	if x == nil {
		return ""
	}
	return *x
}

func getUserLogin(x *github.User) string {
	if x == nil || x.Login == nil {
		return ""
	}
	return *x.Login
}

func getTime(x *time.Time) time.Time {
	if x == nil {
		return time.Time{}
	}
	return *x
}
