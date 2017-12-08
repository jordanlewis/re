package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/fatih/color"
	"github.com/google/go-github/github"
)

func makeReviewTemplate(ctx context.Context, n int) string {
	cmd := exec.Command("git", "fetch", "-f", "https://github.com/cockroachdb/cockroach", "master", fmt.Sprintf("refs/pull/%d/head:refs/reviews/%d", n, n))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	log.Printf("Fetching refs for PR %d", n)
	if err := cmd.Run(); err != nil {
		log.Fatal(fmt.Errorf("invoking fetch: %v", err))
	}

	log.Printf("Fetching details for PR %d", n)
	pr, _, err := client.PullRequests.Get(ctx, projectOwner, projectRepo, n)
	if err != nil {
		log.Fatal(fmt.Errorf("getting pr: %v", err))
	}

	buf := bytes.NewBuffer(make([]byte, 0, 1024))
	printPR(ctx, buf, pr)

	pretty := `--pretty=tformat:commit %H%nAuthor: %an <%ae>%nDate:   %ad%n%n%w(0,4,4)%B`
	cmd = exec.Command("git", "show", "--reverse", pretty, fmt.Sprintf("%s..%s", *pr.Base.SHA, *pr.Head.SHA))
	if err := readPipe(cmd, buf); err != nil {
		log.Fatal(fmt.Errorf("invoking git show: %v", err))
	}

	f, err := ioutil.TempFile("", "re-edit-")
	if err != nil {
		log.Fatal(err)
	}
	if err := ioutil.WriteFile(f.Name(), buf.Bytes(), 0666); err != nil {
		log.Fatal(err)
	}
	filename := f.Name()
	f.Close()

	return filename
}

const timeFormat = "2006-01-02 15:04:05"

const (
	topLevelStartMarker = "# ------ BEGIN  TOP-LEVEL REVIEW COMMENTS ----- #"
	topLevelEndMarker   = "# ------ END OF TOP-LEVEL REVIEW COMMENTS ----- #"
)

func printPR(ctx context.Context, w *bytes.Buffer, pr *github.PullRequest) error {
	// Fool tpope/vim-git's filetype detector for Git commit messages
	fmt.Fprint(w, "commit 0000000000000000000000000000000000000000\n")
	fmt.Fprintf(w, "Author: %s <>\n", getUserLogin(pr.User))
	fmt.Fprintf(w, "Date:   %s\n", getTime(pr.CreatedAt).Format(timeFormat))
	fmt.Fprintf(w, "Title:  %s\n", getString(pr.Title))
	fmt.Fprintf(w, "State:  %s\n", getString(pr.State))
	if pr.MergedAt != nil {
		fmt.Fprintf(w, "Merged: %s\n", getTime(pr.MergedAt).Format(timeFormat))
	}
	if pr.ClosedAt != nil {
		fmt.Fprintf(w, "Closed: %s\n", getTime(pr.ClosedAt).Format(timeFormat))
	}
	fmt.Fprintf(w, "URL:    https://github.com/%s/%s/pulls/%d\n\n", projectOwner, projectRepo, getInt(pr.Number))

	cmd := exec.Command("git", "diff", "--stat", fmt.Sprintf("%s...%s", *pr.Base.SHA, *pr.Head.SHA))
	if err := readPipe(cmd, w); err != nil {
		log.Fatal(err)
	}

	fmt.Fprintf(w, "\nCreated by %s (%s)\n", getUserLogin(pr.User), getTime(pr.CreatedAt).Format(timeFormat))
	if pr.Body != nil {
		text := strings.TrimSpace(*pr.Body)
		if text != "" {
			fmt.Fprintf(w, "\n\t%s\n", wrap(text, "\t"))
		}
	}

	for page := 1; ; {
		list, resp, err := client.Issues.ListComments(ctx, projectOwner, projectRepo, getInt(pr.Number), &github.IssueListCommentsOptions{
			ListOptions: github.ListOptions{
				Page:    page,
				PerPage: 100,
			},
		})
		for _, com := range list {
			fmt.Fprintf(w, "\nComment by %s (%s)\n", getUserLogin(com.User), getTime(com.CreatedAt).Format(timeFormat))
			if com.Body != nil {
				text := strings.TrimSpace(*com.Body)
				if text != "" {
					fmt.Fprintf(w, "\n\t%s\n", wrap(text, "\t"))
				}
			}
		}
		if err != nil {
			return err
		}
		if resp.NextPage < page {
			break
		}
		page = resp.NextPage
	}
	fmt.Fprint(w, "\n")
	fmt.Fprintf(w, `
# Add top-level review comments by typing between the marker lines below.
# Don't modify the markers!

%s
%s

# Add ordinary review comments by typing on a new line below the line of the
# diff you'd like to comment on. Comments may not begin with the special
# characters <space>, +, -, @, or *.
#
# Pre-existing comments are prefixed with *.

`, topLevelStartMarker, topLevelEndMarker)
	return nil
}

var (
	reviewApprove        = "APPROVE"
	reviewRequestChanges = "REQUEST_CHANGES"
	reviewComment        = "COMMENT"
)

func review(prNum int, filename string) *github.PullRequestReviewRequest {
	defer os.Remove(filename)
	stdin := bufio.NewReader(os.Stdin)
	editReview := true
	var request *github.PullRequestReviewRequest
	for {
		if editReview {
			request = parseFileUntilSuccess(filename)
		}
		editReview = true

		fmt.Printf("Submit this review [y,a,r,d,s,p,e,q,?]? ")
		text, err := stdin.ReadString('\n')
		if err != nil && err != io.EOF {
			log.Fatal(err)
		} else if err == io.EOF {
			exitHappy()
		}
		switch text[0] {
		case 'y':
			request.Event = &reviewComment
			return request
		case 'a':
			request.Event = &reviewApprove
			return request
		case 'r':
			request.Event = &reviewRequestChanges
			return request
		case 'd':
			request.Event = nil
			return request
		case 's':
			cpCmd := exec.Command("cp", filename, fmt.Sprintf("%d.redraft", prNum))
			err := cpCmd.Run()
			if err != nil {
				log.Fatal(err)
			}
			exitHappy("Saved draft as", fmt.Sprintf("%d.redraft", prNum))
		case 'p':
			editReview = false
			fmt.Println(request)
			continue
		case 'e':
			continue
		case 'q':
			exitHappy()
		case '?':
			fallthrough
		default:
			editReview = false
			color.Set(color.FgRed, color.Bold)
			fmt.Println("y - submit comments")
			fmt.Println("a - submit and approve")
			fmt.Println("r - submit and request changes")
			fmt.Println("d - publish as draft")
			fmt.Println("s - save review locally and quit; resume with re <pr> resume")
			fmt.Println("p - preview review")
			fmt.Println("e - edit review")
			fmt.Println("q - quit; abandon review")
			fmt.Println("? - print help")
			color.Unset()
			continue
		}
	}
}

func parseFileUntilSuccess(filename string) *github.PullRequestReviewRequest {
	stdin := bufio.NewReader(os.Stdin)
	for {
		updated, err := editFile(filename)
		if err == nil {
			request, err := parseFile(updated)
			if err == nil {
				return request
			}
		}
		fmt.Printf("error parsing file: %s\n", err)
		fmt.Printf("edit again? [Y]/q ")
		text, err := stdin.ReadString('\n')
		if err != nil && err != io.EOF {
			log.Fatal(err)
		} else if err == io.EOF {
			exitHappy()
		}
		text = strings.TrimRight(text, "\n")
		if text == "y" || text == "Y" || text == "" {
			continue
		}
		if text == "q" {
			exitHappy()
		}
	}
}

var commitStart = regexp.MustCompile(`^commit (.*)$`)
var diffStart = `diff --git `
var fileStart = regexp.MustCompile(`^\+\+\+ b\/(.*)$`)
var hunkStart = `@@`

func parseFile(b []byte) (*github.PullRequestReviewRequest, error) {
	dat := string(b)

	commit := ""
	file := ""
	num := 0
	foundFirstHunk := false

	commentStart := -1
	lastCommentStart := -1

	topLevelCommentStart := 0

	review := &github.PullRequestReviewRequest{}

	off := 0
	for _, line := range strings.SplitAfter(dat, "\n") {
		lastCommentStart = commentStart
		commentStart = -1
		if line == "" {
			break
		}

		off += len(line)
		line = strings.TrimRight(line, "\n")

		// Process top level comments.
		if line == topLevelStartMarker {
			topLevelCommentStart = off
			continue
		} else if line == topLevelEndMarker {
			topLevelCommentEnd := off - len(line) - 2
			if topLevelCommentEnd > topLevelCommentStart {
				body := string(dat[topLevelCommentStart:topLevelCommentEnd])
				body += "\n<!-- review by re -->"
				review.Body = &body
			}
			topLevelCommentStart = 0
			continue
		} else if topLevelCommentStart != 0 {
			continue
		}

		// Process commit header.
		commitMatches := commitStart.FindStringSubmatch(line)
		if len(commitMatches) > 1 {
			foundFirstHunk = false
			commit = commitMatches[1]
			review.CommitID = &commit
			continue
		}

		// Process diff header. This means we're in a diff until wee see another
		// diff or commit marker.
		if strings.HasPrefix(line, diffStart) {
			foundFirstHunk = false
			continue
		}

		// Process file header.
		fileMatches := fileStart.FindStringSubmatch(line)
		if len(fileMatches) > 1 {
			file = fileMatches[1]
			continue
		}
		// Process first hunk header.
		if !foundFirstHunk {
			if strings.HasPrefix(line, hunkStart) {
				foundFirstHunk = true
				num = 0
			}
			continue
		}

		// Process special diff first-chars.
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
			review.Comments = append(review.Comments,
				makeDraftReviewComment(file, num))
		}
		c := review.Comments[len(review.Comments)-1]
		body := dat[commentStart : off-1]
		c.Body = &body
	}

	return review, nil
}

func makeDraftReviewComment(path string, position int) *github.DraftReviewComment {
	return &github.DraftReviewComment{
		Path:     &path,
		Position: &position,
	}
}
