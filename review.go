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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"
	"github.com/google/go-github/github"
)

type commitComments map[string]fileComments
type fileComments map[string]lineComments
type lineComments map[int][]*github.PullRequestComment

func (c commitComments) get(commit string, file string, line int) []*github.PullRequestComment {
	if files, ok := c[commit]; ok {
		if lines, ok := files[file]; ok {
			if comments, ok := lines[line]; ok {
				return comments
			}
		}
	}
	return nil
}

func (c commitComments) put(comment *github.PullRequestComment) {
	commit := *comment.CommitID
	file := *comment.Path
	if comment.Position == nil {
		// Outdated comment
		return
	}
	line := *comment.Position
	if _, ok := c[commit]; !ok {
		c[commit] = make(fileComments)
	}
	if _, ok := c[commit][file]; !ok {
		c[commit][file] = make(lineComments)
	}
	c[commit][file][line] = append(c[commit][file][line], comment)
}

// topLevelComment represents either a review comment or an issue comment.
type topLevelComment struct {
	body      string
	author    string
	createdAt time.Time
	// Only for reviews
	state    string
	commitID string
}

type topLevelComments []topLevelComment

func (c topLevelComments) Len() int           { return len(c) }
func (c topLevelComments) Swap(i, j int)      { c[i], c[j] = c[j], c[i] }
func (c topLevelComments) Less(i, j int) bool { return c[i].createdAt.Before(c[j].createdAt) }

func makeReviewTemplate(ctx context.Context, n int) string {
	log.Printf("Fetching details for PR %d", n)
	var wg sync.WaitGroup
	var showWg sync.WaitGroup
	wg.Add(6)
	showWg.Add(2)
	var pr *github.PullRequest
	go func() {
		start := time.Now()
		var err error
		pr, _, err = client.PullRequests.Get(ctx, projectOwner, projectRepo, n)
		if err != nil {
			log.Fatal(fmt.Errorf("getting pr: %v", err))
		}
		showWg.Done()
		wg.Done()
		log.Printf("Fetched pr in %v", time.Now().Sub(start))
	}()
	reviews := make([]*github.PullRequestReview, 0, 10)
	go func() {
		start := time.Now()
		for page := 1; ; {
			list, resp, err := client.PullRequests.ListReviews(ctx, projectOwner, projectRepo, n, &github.ListOptions{
				Page:    page,
				PerPage: 100,
			})
			if err != nil {
				log.Fatal(fmt.Errorf("invoking list reviews: %v", err))
			}
			reviews = append(reviews, list...)
			if resp.NextPage < page {
				break
			}
			page = resp.NextPage
		}
		wg.Done()
		log.Printf("Fetched reviews in %v", time.Now().Sub(start))
	}()
	go func() {
		start := time.Now()
		repoURL := fmt.Sprintf("https://github.com/%s/%s", projectOwner, projectRepo)
		cmd := exec.Command("git", "fetch", "-f", repoURL, "master", fmt.Sprintf("refs/pull/%d/head:refs/reviews/%d", n, n))
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			log.Fatal(fmt.Errorf("invoking fetch: %v", err))
		}
		showWg.Done()
		wg.Done()
		log.Printf("Fetched refs in %v", time.Now().Sub(start))
	}()
	diffBuf := bytes.NewBuffer(make([]byte, 0, 1024))
	go func() {
		// Can't show until fetch is performed and PR is fetched.
		showWg.Wait()
		start := time.Now()
		pretty := `--pretty=format:commit %H%nAuthor: %an <%ae>%nDate:   %ad%n%n%w(0,4,4)%B`
		cmd := exec.Command("git", "show", "--reverse", pretty, fmt.Sprintf("%s..%s", *pr.Base.SHA, *pr.Head.SHA))
		if err := readPipe(cmd, diffBuf); err != nil {
			log.Fatal(fmt.Errorf("invoking git show: %v", err))
		}
		wg.Done()
		log.Printf("Showed diffs in %v", time.Now().Sub(start))
	}()
	issueComments := make([]*github.IssueComment, 0, 10)
	go func() {
		start := time.Now()
		for page := 1; ; {
			list, resp, err := client.Issues.ListComments(ctx, projectOwner, projectRepo, n, &github.IssueListCommentsOptions{
				ListOptions: github.ListOptions{
					Page:    page,
					PerPage: 100,
				},
			})
			if err != nil {
				log.Fatal(fmt.Errorf("invoking list issue comments: %v", err))
			}
			issueComments = append(issueComments, list...)
			if resp.NextPage < page {
				break
			}
			page = resp.NextPage
		}
		log.Printf("Fetched issue comments in %v", time.Now().Sub(start))
		wg.Done()
	}()
	reviewComments := make(commitComments)
	go func() {
		start := time.Now()
		for page := 1; ; {
			list, resp, err := client.PullRequests.ListComments(ctx, projectOwner, projectRepo, n, &github.PullRequestListCommentsOptions{
				ListOptions: github.ListOptions{
					Page:    page,
					PerPage: 100,
				},
			})
			if err != nil {
				log.Fatal(fmt.Errorf("invoking list issue comments: %v", err))
			}
			for _, comment := range list {
				reviewComments.put(comment)
			}
			if resp.NextPage < page {
				break
			}
			page = resp.NextPage
		}
		log.Printf("Fetched review comments in %v", time.Now().Sub(start))
		wg.Done()
	}()
	wg.Wait()

	topLevelComments := make(topLevelComments, 0, len(reviews)+len(issueComments))
	for _, r := range reviews {
		topLevelComments = append(topLevelComments, topLevelComment{
			body:      getString(r.Body),
			createdAt: getTime(r.SubmittedAt),
			author:    getUserLogin(r.User),
			state:     getString(r.State),
			commitID:  getString(r.CommitID),
		})
	}
	for _, c := range issueComments {
		topLevelComments = append(topLevelComments, topLevelComment{
			body:      getString(c.Body),
			createdAt: getTime(c.CreatedAt),
			author:    getUserLogin(c.User),
		})
	}
	sort.Sort(topLevelComments)

	buf := bytes.NewBuffer(make([]byte, 0, 1024))
	printPR(ctx, buf, pr, topLevelComments)

	commit := ""
	file := ""
	num := 0
	foundFirstHunk := false
	// Parse the `git diff` output, output line-by-line to the review template,
	// and insert inline comments where they're supposed to go.
	for _, line := range strings.SplitAfter(diffBuf.String(), "\n") {
		if line == "" {
			break
		}
		buf.WriteString(line)
		line = strings.TrimRight(line, "\n")

		// Process commit header.
		commitMatches := commitStart.FindStringSubmatch(line)
		if len(commitMatches) > 1 {
			foundFirstHunk = false
			commit = commitMatches[1]
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
		num++
		if comments := reviewComments.get(commit, file, num); comments != nil {
			fmt.Fprintf(buf, "%s\n", inlineStartMarker)
			for _, comment := range comments {
				fmt.Fprintf(buf, "* Comment by @%s (%s)", getUserLogin(comment.User), getTime(comment.CreatedAt).Format(timeFormat))
				if comment.InReplyTo == nil {
					fmt.Fprintf(buf, " thread %d", *comment.ID)
				}
				buf.WriteString("\n")
				fmt.Fprintf(buf, "*\t%s\n", wrap(*comment.Body, "*\t"))
			}
			fmt.Fprintf(buf, "%s\n", inlineEndMarker)
		}
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

var (
	topLevelStartMarker = "# ------ BEGIN  TOP-LEVEL REVIEW COMMENTS ----- #"
	topLevelEndMarker   = "# ------ END OF TOP-LEVEL REVIEW COMMENTS ----- #"
	inlineStartMarker   = strings.Repeat("*", 79) + "v"
	inlineEndMarker     = strings.Repeat("*", 79) + "^"
)

func printPR(ctx context.Context, w *bytes.Buffer, pr *github.PullRequest, comments topLevelComments) error {
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
	fmt.Fprintf(w, "URL:    https://github.com/%s/%s/pull/%d\n\n", projectOwner, projectRepo, getInt(pr.Number))

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

	for _, com := range comments {
		text := strings.TrimSpace(com.body)
		if text == "" {
			continue
		}
		if strings.Contains(text, "<!-- Reviewable:start -->") {
			// Don't print "This change is Reviewable" message
			continue
		}
		if strings.Contains(text, "<!-- Sent from Reviewable.io -->") {
			// TODO(jordan) parse Reviewable comments into inlie comments.
		}

		action := "Comment"
		switch com.state {
		case reviewApprove:
			action = "Approved"
		case reviewRequestChanges:
			action = "Changes requested"
		case reviewPending:
			action = "Draft comment"
		}
		fmt.Fprintf(w, "\n%s by %s (%s)\n", action, com.author, com.createdAt.Format(timeFormat))
		fmt.Fprintf(w, "\n\t%s\n", wrap(text, "\t"))
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
	reviewPending        = "PENDING"
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
var threadId = regexp.MustCompile(`^\* Comment by @\w+ \([^\)]+\) thread (\d+)$`)

func parseFile(b []byte) (*github.PullRequestReviewRequest, error) {
	dat := string(b)

	commit := ""
	file := ""
	num := 0
	foundFirstHunk := false

	commentStart := -1
	lastCommentStart := -1

	topLevelCommentStart := 0

	lastInlineCommentId := 0

	reviews := []*github.PullRequestReviewRequest{
		&github.PullRequestReviewRequest{},
	}
	review := reviews[0]

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

		if line == inlineStartMarker {
			continue
		} else if line == inlineEndMarker {
			lastInlineCommentId = 0
			continue
		}
		threadIdMatches := threadId.FindStringSubmatch(line)
		if len(threadIdMatches) > 1 {
			var err error
			lastInlineCommentId, err = strconv.Atoi(threadIdMatches[1])
			if err != nil {
				return nil, err
			}
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

		if len(line) == 0 {
			// Empty line. Skip.
			continue
		}

		// Process special diff first-chars.
		switch line[0] {
		case '+', '-', ' ', '@':
			num++
			continue
		case '*', '\t':
			// Old comment
			continue
		}

		// We found a comment!
		commentStart = lastCommentStart
		if commentStart == -1 {
			commentStart = off - len(line) - 1
			comment := makeDraftReviewComment(file, num)
			if lastInlineCommentId != 0 {
				/* TODO(jordan) figure out how to send raft replies
				cId := lastInlineCommentId
				comment.InReplyTo = &cId
				comment.Path = nil
				comment.Position = nil
				*/
			}
			review.Comments = append(review.Comments, comment)
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
