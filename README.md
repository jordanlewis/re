`re` is a code review tool.  It lets you do Github code reviews from
the terminal using your favorite `$EDITOR`.

## Installation

Install re:

`go get github.com/jordanlewis/re`

Add your GitHub API token to `~/.github-issue-token`.

## Usage

Use the `-p` option to specify which GitHub project to search for PRs in. If
you don't specify one, `re` will attempt to infer a GitHub project by looking
at the `origin` remote in the repo that it's invoked from.

To see all of the PRs you are working on, run:

    $ re -p cockroachdb/docs
    Created by me:
     3530  rmloveland  Document pipelining of transactional writes
     3528  rmloveland  Fix typo: use 'decrease' instead of 'increase'
     3506  rmloveland  Add docs to describe new default database(s)
     3492  rmloveland  Document online schema changes

    Involving me:
     3546  mberhault   Add more details about encryption status.
     3538  lhirata     Convert computed column to regular column

To add your review to a PR, run:

    $ re -p cockroachdb/docs 3538

This will open a text file in your editor showing a git diff with some
specialized instructions, which are reproduced below:

    # Add top-level review comments by typing between the marker lines below.
    # Don't modify the markers!

    # ------ BEGIN  TOP-LEVEL REVIEW COMMENTS ----- #
    # ------ END OF TOP-LEVEL REVIEW COMMENTS ----- #

    # Add ordinary review comments by typing on a new line below the line of the
    # diff you'd like to comment on. Comments may not begin with the special
    # characters <space>, +, -, @, or *.
    #
    # Pre-existing comments are prefixed with *.

Follow the instructions to add your review.  Exit your editor, and you
will be prompted about what to do with your changes like so:

    Submit this review [y,a,r,d,s,p,e,q,?]?

Where the options are:

- y - submit comments
- a - submit and approve
- r - submit and request changes
- d - publish as draft
- s - save review locally and quit; resume with re <pr> resume
- p - preview review
- e - edit review
- q - quit; abandon review
- ? - print help
